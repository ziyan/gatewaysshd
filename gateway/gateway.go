package gateway

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	logging "github.com/op/go-logging"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

var log = logging.MustGetLogger("gateway")

const (
	// how often each node refreshes online_at for its connected users
	onlineHeartbeatInterval = 30 * time.Second

	// how long after the last heartbeat a user or node still counts as online;
	// must be comfortably larger than the heartbeat intervals (onlineHeartbeatInterval
	// for users, PeerDiscoveryInterval for nodes), and bounds how long a crashed
	// node keeps itself and its users appearing online
	onlineStaleThreshold = 90 * time.Second
)

type Settings struct {
	// version string
	Version string

	// id of this node, empty disables node registration and mesh tunneling
	NodeID string

	// address where peer nodes can reach this node, empty means other nodes
	// cannot dial this node
	NodeAddress string

	// host public key of this node, published to the node table so dialing
	// peers can pin it
	HostPublicKey ssh.PublicKey

	// certificate signer used to authenticate to other nodes, a user
	// certificate signed by the peer certificate authority with the "peer"
	// principal and the node id as key id, nil disables outbound peering
	NodeSigner ssh.Signer

	// address of the local postgres database bridged for peer nodes, empty
	// refuses postgres tunnels
	PostgresAddress string

	// how often to look for new peer nodes and refresh this node's online
	// heartbeat, defaults to 15 seconds; must stay well below
	// onlineStaleThreshold or other nodes will consider this node dead
	PeerDiscoveryInterval time.Duration

	// revokes a user's cached login flags on this node, called by kickUser
	// so a kicked user's next login re-reads the database, optional
	RevokeLoginFlags func(userId string)
}

// an instance of gateway, contains runtime states
type Gateway interface {
	Close()

	HandleConnection(net.Conn)
	HandleSocksConnection(net.Conn)
	HTTPProxyHandler() http.Handler
	ScavengeConnections(time.Duration)

	ListUsers(context.Context) (interface{}, error)
	ListOnlineUsers(context.Context) (interface{}, error)
	ListLocalOnlineUsers(context.Context) (interface{}, error)
	GetUser(context.Context, string) (interface{}, error)
	GetUserScreenshot(context.Context, string) ([]byte, error)
}

type gateway struct {
	database         db.Database
	sshConfig        *ssh.ServerConfig
	settings         *Settings
	connectionsIndex map[string][]*connection
	connectionsList  []*connection
	lock             sync.Mutex

	// map from peer node id to outbound peer, protected by peersLock
	peers     map[string]*peer
	peersLock sync.Mutex

	// remembers which node a user is on for mesh tunnel routing
	routes *routeCache

	// lifetime management for background loops
	done      chan struct{}
	closeOnce sync.Once
	waitGroup sync.WaitGroup
}

// creates a new instance of gateway
func Open(database db.Database, sshConfig *ssh.ServerConfig, settings *Settings) (Gateway, error) {
	self := &gateway{
		database:         database,
		sshConfig:        sshConfig,
		settings:         settings,
		connectionsIndex: make(map[string][]*connection),
		connectionsList:  make([]*connection, 0),
		peers:            make(map[string]*peer),
		routes:           newRouteCache(),
		done:             make(chan struct{}),
	}
	if settings.NodeID != "" {
		if err := self.registerNode(context.Background(), true); err != nil {
			return nil, err
		}
		self.waitGroup.Add(1)
		go func() {
			defer deferutil.Recover()
			defer self.waitGroup.Done()
			self.runPeers(context.Background())
		}()
	}
	self.waitGroup.Add(1)
	go func() {
		defer deferutil.Recover()
		defer self.waitGroup.Done()
		self.runOnlineHeartbeat(context.Background())
	}()
	return self, nil
}

// close the gateway instance
func (self *gateway) Close() {
	self.closeOnce.Do(func() {
		close(self.done)
	})

	for _, connection := range self.listConnections() {
		connection.close()
	}

	for _, peer := range self.listPeers() {
		peer.close()
	}

	self.waitGroup.Wait()

	// mark this node as offline in the database
	if err := self.registerNode(context.Background(), false); err != nil {
		log.Warningf("failed to mark node as offline: %s", err)
	}
}

// handle an incoming ssh connection
func (self *gateway) HandleConnection(socket net.Conn) {
	log.Infof("new tcp connection: remote = %s, local = %s", socket.RemoteAddr(), socket.LocalAddr())
	defer func() {
		if socket != nil {
			if err := socket.Close(); err != nil {
				log.Warningf("failed to close connection: %s", err)
			}
		}
	}()

	usage := newUsage()
	conn, channels, requests, err := ssh.NewServerConn(wrapConn(socket, usage), self.sshConfig)
	if err != nil {
		log.Warningf("failed during ssh handshake: %s", err)
		return
	}
	defer func() {
		if conn != nil {
			if err := conn.Close(); err != nil {
				log.Warningf("failed to close connection: %s", err)
			}
		}
	}()

	// create a connection and handle it
	handleConnection(self, conn, channels, requests, usage)

	// don't close connection on success
	conn = nil
	socket = nil
}

// add connection to the list of connections
func (self *gateway) addConnection(addedConnection *connection) {
	self.lock.Lock()
	defer self.lock.Unlock()

	self.connectionsIndex[addedConnection.user] = append([]*connection{addedConnection}, self.connectionsIndex[addedConnection.user]...)
	self.connectionsList = append([]*connection{addedConnection}, self.connectionsList...)
}

func (self *gateway) deleteConnection(deletedConnection *connection) {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]*connection, 0, len(self.connectionsIndex[deletedConnection.user]))
	for _, connection := range self.connectionsIndex[deletedConnection.user] {
		if connection != deletedConnection {
			connections = append(connections, connection)
		}
	}
	self.connectionsIndex[deletedConnection.user] = connections

	connections = make([]*connection, 0, len(self.connectionsList))
	for _, connection := range self.connectionsList {
		if connection != deletedConnection {
			connections = append(connections, connection)
		}
	}
	self.connectionsList = connections
}

func (self *gateway) lookupConnectionService(host string, port uint16) (*connection, string, uint16) {
	self.lock.Lock()
	defer self.lock.Unlock()

	parts := strings.Split(host, ".")
	for index := range parts {
		host := strings.Join(parts[:index], ".")
		user := strings.Join(parts[index:], ".")

		for _, connection := range self.connectionsIndex[user] {
			if connection.lookupService(host, port) {
				log.Debugf("lookup: found service: connection = %s, host = %s, port = %d", connection, host, port)
				return connection, host, port
			}
		}
	}

	log.Debugf("lookup: failed to find service: host = %s, port = %d", host, port)
	return nil, "", 0
}

// lookupRemotePeer finds the peer of the node the target user last connected
// to, so the tunnel can be forwarded across the mesh. The host is split the
// same way as lookupConnectionService to find the user name candidates.
func (self *gateway) lookupRemotePeer(ctx context.Context, host string) *peer {
	if self.settings.NodeID == "" {
		return nil // mesh disabled
	}
	parts := strings.Split(host, ".")
	for index := range parts {
		user := strings.Join(parts[index:], ".")
		if user == "" {
			continue
		}
		nodeId, cached := self.routes.get(user)
		if !cached {
			var err error
			nodeId, err = self.database.GetUserNodeID(ctx, user)
			if err != nil {
				// a transient error on one candidate must not abort resolving
				// the remaining, more-specific user suffixes
				log.Warningf("failed to look up user %q for mesh tunneling: %s", user, err)
				continue
			}
			if nodeId != "" && nodeId != self.settings.NodeID {
				// only remember routable results: a user without a node may
				// connect somewhere any moment and must be seen immediately
				self.routes.put(user, nodeId)
			}
		}
		if nodeId == "" || nodeId == self.settings.NodeID {
			continue
		}
		if peer := self.getPeer(nodeId); peer != nil {
			log.Debugf("lookup: found remote node for host %q: node = %s", host, nodeId)
			return peer
		}
		log.Debugf("lookup: user %q is on node %s but no peer connection is available", user, nodeId)
	}
	return nil
}

// openServiceTunnel opens a tunnel to the exposed service host:port on behalf
// of an external proxy client (socks/http). It mirrors the routing in
// connection.handleTunnelChannel: a locally-connected service first, otherwise
// a service on a peer node. originAddress/originPort describe the proxy client.
func (self *gateway) openServiceTunnel(host string, port uint16, originAddress string, originPort uint32) (*tunnel, error) {
	metadata := map[string]interface{}{
		"origin": originAddress,
		"service": map[string]interface{}{
			"host": host,
			"port": port,
		},
	}
	if otherConnection, serviceHost, servicePort := self.lookupConnectionService(host, port); otherConnection != nil {
		return otherConnection.openTunnel("forwarded-tcpip", marshalTunnelData(&tunnelData{
			Host:          serviceHost,
			Port:          uint32(servicePort),
			OriginAddress: originAddress,
			OriginPort:    originPort,
		}), metadata)
	}
	lookupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	remotePeer := self.lookupRemotePeer(lookupContext, host)
	cancel()
	if remotePeer != nil {
		tunnel, err := remotePeer.openTunnel("direct-tcpip", marshalTunnelData(&tunnelData{
			Host:          host,
			Port:          uint32(port),
			OriginAddress: originAddress,
			OriginPort:    originPort,
		}), metadata)
		if err != nil {
			// the user may have left that node, forget the cached route so
			// the next attempt re-reads the database
			self.invalidateRoutes(host)
		}
		return tunnel, err
	}
	return nil, ErrServiceNotFound
}

// invalidateRoutes forgets the cached routes for the host's user candidates,
// called after a peer rejected a tunnel so a stale route is not retried for
// the rest of its lifetime.
func (self *gateway) invalidateRoutes(host string) {
	parts := strings.Split(host, ".")
	for index := range parts {
		user := strings.Join(parts[index:], ".")
		if user != "" {
			self.routes.invalidate(user)
		}
	}
}

// returns a list of connections
func (self *gateway) listConnections() []*connection {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]*connection, len(self.connectionsList))
	copy(connections, self.connectionsList)
	return connections
}

// scavenge timed out connections
func (self *gateway) ScavengeConnections(timeout time.Duration) {
	for _, connection := range self.listConnections() {
		idle := time.Since(connection.getUsedAt())
		if idle > timeout {
			log.Infof("scavenge: connection %s timed out after %s", connection, idle)
			connection.close()
		}
	}
}

func (self *gateway) kickUser(user string) error {
	// a kick is the immediate removal tool: also drop the user's cached
	// login flags so an instant reconnect re-reads the database
	if self.settings.RevokeLoginFlags != nil {
		self.settings.RevokeLoginFlags(user)
	}
	for _, connection := range self.listConnections() {
		if connection.user == user {
			log.Infof("kick: closing connection %s", connection)
			connection.close()
		}
	}
	return nil
}

func (self *gateway) gatherStatus(user string) map[string]interface{} {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]interface{}, 0, len(self.connectionsList))
	for _, connection := range self.connectionsList {
		if user == "" || connection.user == user {
			connections = append(connections, connection.gatherStatus())
		}
	}

	return map[string]interface{}{
		"connections": connections,
	}
}

// isHeartbeatFresh reports whether an online heartbeat is recent enough to
// count as online. Heartbeats are stamped with the writing node's clock, so
// node clocks must be kept in sync (ntp) well within onlineStaleThreshold.
func isHeartbeatFresh(onlineAt time.Time) bool {
	return !onlineAt.IsZero() && time.Since(onlineAt) < onlineStaleThreshold
}

// isUserOnline reports whether the user's online_at is fresh enough to count
// as online. online_at is refreshed on a heartbeat by whichever node the user
// is connected to, so this is mesh-wide.
func isUserOnline(user *db.User) bool {
	return isHeartbeatFresh(user.OnlineAt)
}

// connectedUserIds returns the distinct users currently connected to this
// node. peer connections are indexed under an empty user and excluded.
func (self *gateway) connectedUserIds() []string {
	self.lock.Lock()
	defer self.lock.Unlock()

	userIds := make([]string, 0, len(self.connectionsIndex))
	for userId, connections := range self.connectionsIndex {
		if userId == "" || len(connections) == 0 {
			continue
		}
		userIds = append(userIds, userId)
	}
	return userIds
}

// runOnlineHeartbeat periodically refreshes online_at for the users connected
// to this node, so their online status is visible mesh-wide and ages out on
// its own if this node stops beating.
func (self *gateway) runOnlineHeartbeat(ctx context.Context) {
	for {
		select {
		case <-self.done:
			return
		case <-time.After(onlineHeartbeatInterval):
			userIds := self.connectedUserIds()
			if len(userIds) == 0 {
				continue
			}
			// bound each beat so a wedged database cannot block this loop, and
			// with it Close(), forever
			beatContext, cancel := context.WithTimeout(ctx, onlineHeartbeatInterval)
			err := self.database.MarkUsersOnline(beatContext, userIds, self.settings.NodeID, onlineStaleThreshold)
			cancel()
			if err != nil {
				log.Warningf("failed to refresh online users: %s", err)
				continue
			}
			// a user may have disconnected while the update was in flight,
			// right after releaseUserNode cleared their node_id: the update
			// would then have re-adopted them onto this node, so release
			// everyone from the batch that is no longer connected
			stillConnected := make(map[string]struct{}, len(userIds))
			for _, userId := range self.connectedUserIds() {
				stillConnected[userId] = struct{}{}
			}
			for _, userId := range userIds {
				if _, ok := stillConnected[userId]; !ok {
					self.releaseUserNode(userId)
				}
			}
		}
	}
}

// releaseUserNode clears the user's node_id after their last connection to
// this node closed, so a node the user is still connected to can adopt them
// on its next heartbeat instead of mesh tunnels being routed here.
func (self *gateway) releaseUserNode(userId string) {
	if self.settings.NodeID == "" || userId == "" {
		return
	}
	self.lock.Lock()
	remaining := len(self.connectionsIndex[userId])
	self.lock.Unlock()
	if remaining > 0 {
		return
	}
	if err := self.database.ClearUserNodeID(context.Background(), userId, self.settings.NodeID); err != nil {
		log.Warningf("failed to release node of user %q: %s", userId, err)
	}
}

// userListResponse is the common shape of the user listing commands
func userListResponse(users []*db.User) interface{} {
	if users == nil {
		// marshal an empty list as [] instead of null
		users = []*db.User{}
	}
	return map[string]interface{}{
		"users": users,
		"meta": map[string]interface{}{
			"totalCount": len(users),
		},
	}
}

// onlineUserListResponse strips the heavy status and marks every user
// online, the common response shaping of the online listing commands
func onlineUserListResponse(users []*db.User) interface{} {
	for _, user := range users {
		user.Status = nil
		user.Online = true
	}
	return userListResponse(users)
}

func (self *gateway) ListUsers(ctx context.Context) (interface{}, error) {
	users, err := self.database.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	// blend in this node's live connections, like GetUser, so local state
	// stays accurate even when the heartbeat lags; users on other nodes are
	// covered by the heartbeat
	connected := make(map[string]struct{})
	for _, userId := range self.connectedUserIds() {
		connected[userId] = struct{}{}
	}
	for _, user := range users {
		user.Status = nil
		_, local := connected[user.ID]
		user.Online = local || isUserOnline(user)
	}
	return userListResponse(users), nil
}

func (self *gateway) ListOnlineUsers(ctx context.Context) (interface{}, error) {
	// online status is mesh-wide, derived purely from heartbeat freshness in
	// sql, which includes users connected to other nodes
	users, err := self.database.ListOnlineUsers(ctx, time.Now().Add(-onlineStaleThreshold))
	if err != nil {
		return nil, err
	}
	return onlineUserListResponse(users), nil
}

func (self *gateway) ListLocalOnlineUsers(ctx context.Context) (interface{}, error) {
	// only the users connected to this node, taken from the live connection
	// index, so a single id-set query fetches their records
	users, err := self.database.GetUsers(ctx, self.connectedUserIds())
	if err != nil {
		return nil, err
	}
	return onlineUserListResponse(users), nil
}

func (self *gateway) GetUser(ctx context.Context, userId string) (interface{}, error) {
	user, err := self.database.GetUser(ctx, userId)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}

	var connections []*connectionStatus
	func() {
		self.lock.Lock()
		defer self.lock.Unlock()

		for _, connection := range self.connectionsList {
			if connection.user == user.ID {
				connections = append(connections, connection.gatherStatus())
			}
		}
	}()

	// online if connected here or seen recently on any node in the mesh
	user.Online = len(connections) > 0 || isUserOnline(user)
	user.Connections = connections

	return map[string]interface{}{
		"user": user,
	}, nil
}

func (self *gateway) GetUserScreenshot(ctx context.Context, userId string) ([]byte, error) {
	// fetch only the blob instead of the whole row
	return self.database.GetUserScreenshot(ctx, userId)
}
