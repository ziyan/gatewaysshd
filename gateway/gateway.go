package gateway

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	logging "github.com/op/go-logging"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

var log = logging.MustGetLogger("gateway")

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

	// how often to look for new peer nodes, defaults to 15 seconds
	PeerDiscoveryInterval time.Duration
}

// an instance of gateway, contains runtime states
type Gateway interface {
	Close()

	HandleConnection(net.Conn)
	ScavengeConnections(time.Duration)

	ListUsers(context.Context) (interface{}, error)
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
		done:             make(chan struct{}),
	}
	if settings.NodeID != "" {
		if err := self.registerNode(context.Background(), true, true); err != nil {
			return nil, err
		}
		self.waitGroup.Add(1)
		go func() {
			defer deferutil.Recover()
			defer self.waitGroup.Done()
			self.runPeers(context.Background())
		}()
	}
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
	if err := self.registerNode(context.Background(), false, false); err != nil {
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
		model, err := self.database.GetUser(ctx, user)
		if err != nil {
			// a transient error on one candidate must not abort resolving the
			// remaining, more-specific user suffixes
			log.Warningf("failed to look up user %q for mesh tunneling: %s", user, err)
			continue
		}
		if model == nil || model.NodeID == "" || model.NodeID == self.settings.NodeID {
			continue
		}
		if peer := self.getPeer(model.NodeID); peer != nil {
			log.Debugf("lookup: found remote node for host %q: node = %s", host, model.NodeID)
			return peer
		}
		log.Debugf("lookup: user %q is on node %s but no peer connection is available", user, model.NodeID)
	}
	return nil
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

func (self *gateway) ListUsers(ctx context.Context) (interface{}, error) {
	users, err := self.database.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	func() {
		self.lock.Lock()
		defer self.lock.Unlock()
		for _, user := range users {
			user.Status = nil
			user.Online = false
			for _, connection := range self.connectionsList {
				if connection.user == user.ID {
					user.Online = true
					break
				}
			}
		}
	}()
	return map[string]interface{}{
		"users": users,
		"meta": map[string]interface{}{
			"totalCount": len(users),
		},
	}, nil
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

	user.Online = len(connections) > 0
	user.Connections = connections

	return map[string]interface{}{
		"user": user,
	}, nil
}

func (self *gateway) GetUserScreenshot(ctx context.Context, userId string) ([]byte, error) {
	user, err := self.database.GetUser(ctx, userId)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}
	return user.Screenshot, nil
}
