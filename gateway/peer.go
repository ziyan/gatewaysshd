package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/util/deferutil"
	"github.com/ziyan/gatewaysshd/util/sshconfig"
)

// peer represents an outbound ssh connection to another node in the mesh
type peer struct {
	gateway *gateway
	nodeId  string
	client  *ssh.Client

	closeOnce sync.Once
}

func newPeer(gateway *gateway, nodeId string, client *ssh.Client) *peer {
	return &peer{
		gateway: gateway,
		nodeId:  nodeId,
		client:  client,
	}
}

func (self *peer) String() string {
	return fmt.Sprintf("peer(nodeId=%q)", self.nodeId)
}

// open a channel to the peer node
func (self *peer) openTunnel(channelType string, extraData []byte, metadata map[string]interface{}) (*tunnel, error) {
	channel, requests, err := self.client.OpenChannel(channelType, extraData)
	if err != nil {
		return nil, err
	}

	tunnel := newTunnel(nil, channel, channelType, extraData, metadata)
	go func() {
		defer deferutil.Recover()
		tunnel.handleRequests(requests)
	}()
	return tunnel, nil
}

func (self *peer) ping() error {
	if _, _, err := self.client.SendRequest("keepalive@gatewaysshd", true, nil); err != nil {
		return err
	}
	return nil
}

func (self *peer) close() {
	self.closeOnce.Do(func() {
		if err := self.client.Close(); err != nil {
			log.Debugf("%s: failed to close client: %s", self, err)
		}
	})
}

// register this node in the database so other nodes can discover it. OnlineAt
// is refreshed on every heartbeat while online, so peers can tell a live node
// from one that crashed while its row still says Online=true.
func (self *gateway) registerNode(ctx context.Context, online bool) error {
	if self.settings.NodeID == "" {
		return nil
	}
	hostPublicKey := ""
	if self.settings.HostPublicKey != nil {
		hostPublicKey = string(ssh.MarshalAuthorizedKey(self.settings.HostPublicKey))
	}
	now := time.Now().In(time.Local)
	if _, err := self.database.PutNode(ctx, self.settings.NodeID, func(node *db.Node) error {
		node.Address = self.settings.NodeAddress
		node.HostPublicKey = hostPublicKey
		node.Online = online
		node.OnlineAt = now
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// isNodeOnline reports whether a node registration is live: marked online and
// heartbeat fresh. A crashed node cannot mark itself offline, so a stale
// heartbeat means dead, keeping peers from redialing it forever.
func isNodeOnline(node *db.Node) bool {
	return node.Online && !node.OnlineAt.IsZero() && time.Since(node.OnlineAt) < onlineStaleThreshold
}

// discover other online nodes from the database and connect to them
func (self *gateway) discoverPeers(ctx context.Context) error {
	if self.settings.NodeSigner == nil {
		return nil // outbound peering disabled
	}
	nodes, err := self.database.ListNodes(ctx)
	if err != nil {
		return err
	}

	for _, node := range nodes {
		if !isNodeOnline(node) || node.Address == "" || node.ID == self.settings.NodeID {
			continue // cannot connect
		}
		if self.getPeer(node.ID) != nil {
			continue // already connected
		}
		self.waitGroup.Add(1)
		go func() {
			defer deferutil.Recover()
			defer self.waitGroup.Done()
			self.connectToPeer(node)
		}()
	}

	return nil
}

func (self *gateway) connectToPeer(node *db.Node) {
	pinnedHostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(node.HostPublicKey))
	if err != nil {
		log.Warningf("failed to parse host public key of peer %s: %s", node.ID, err)
		return
	}

	config := sshconfig.NewPeerClientConfig(self.settings.NodeSigner, pinnedHostKey)

	client, err := ssh.Dial("tcp", node.Address, config)
	if err != nil {
		log.Warningf("failed to connect to peer %s at %s: %s", node.ID, node.Address, err)
		return
	}
	peer := newPeer(self, node.ID, client)

	// add to peers map
	if !self.addPeer(peer) {
		peer.close() // already connected, close this new connection
		return
	}

	// remove from peers map at the end
	defer self.removePeer(peer)
	defer peer.close()

	// close the peer when the gateway shuts down so client.Wait() below
	// unblocks even for a peer registered after Close()'s snapshot. tracked in
	// the waitGroup (connectToPeer holds a token throughout, so Add cannot race
	// Wait at zero) so Close()'s barrier awaits it too.
	watchDone := make(chan struct{})
	defer close(watchDone)
	self.waitGroup.Add(1)
	go func() {
		defer deferutil.Recover()
		defer self.waitGroup.Done()
		select {
		case <-self.done:
			peer.close()
		case <-watchDone:
		}
	}()

	// wait until connection is done
	log.Noticef("connected to peer %s at %s", node.ID, node.Address)
	err = client.Wait()
	log.Noticef("peer connection to %s at %s closed: %s", node.ID, node.Address, err)
}

func (self *gateway) runPeers(ctx context.Context) {
	interval := self.settings.PeerDiscoveryInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if err := self.discoverPeers(ctx); err != nil {
		log.Warningf("failed to discover peers: %s", err)
	}
	for {
		select {
		case <-self.done:
			return
		case <-time.After(interval):
			if err := self.registerNode(ctx, true); err != nil {
				log.Warningf("failed to register node: %s", err)
			}
			if err := self.discoverPeers(ctx); err != nil {
				log.Warningf("failed to discover peers: %s", err)
			}
			for _, peer := range self.listPeers() {
				if err := peer.ping(); err != nil {
					// evict the dead peer so the next discovery reconnects it,
					// otherwise getPeer keeps returning a broken connection
					log.Warningf("evicting unresponsive peer node %q: %s", peer.nodeId, err)
					self.removePeer(peer)
					peer.close()
				}
			}
		}
	}
}

// returns a list of peers
func (self *gateway) listPeers() []*peer {
	self.peersLock.Lock()
	defer self.peersLock.Unlock()

	peers := make([]*peer, 0, len(self.peers))
	for _, peer := range self.peers {
		peers = append(peers, peer)
	}
	return peers
}

func (self *gateway) getPeer(nodeId string) *peer {
	self.peersLock.Lock()
	defer self.peersLock.Unlock()

	return self.peers[nodeId]
}

func (self *gateway) addPeer(peer *peer) bool {
	self.peersLock.Lock()
	defer self.peersLock.Unlock()

	if _, ok := self.peers[peer.nodeId]; ok {
		return false
	}
	self.peers[peer.nodeId] = peer
	return true
}

func (self *gateway) removePeer(peer *peer) {
	self.peersLock.Lock()
	defer self.peersLock.Unlock()

	if self.peers[peer.nodeId] == peer {
		delete(self.peers, peer.nodeId)
	}
}
