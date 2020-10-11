package gateway

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/segmentio/ksuid"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
)

var (
	ErrServiceAlreadyRegistered = errors.New("gatewaysshd: service already registered")
	ErrServiceNotFound          = errors.New("gatewaysshd: service not found")
)

// a ssh connection
type connection struct {
	id                   string
	gateway              *gateway
	conn                 *ssh.ServerConn
	user                 string
	remoteAddr           net.Addr
	localAddr            net.Addr
	tunnels              []*tunnel
	tunnelsClosed        uint64
	services             map[string]map[uint16]bool
	lock                 sync.Mutex
	closeOnce            sync.Once
	waitGroup            sync.WaitGroup
	usage                *usageStats
	permitPortForwarding bool
	administrator        bool
}

func newConnection(gateway *gateway, conn *ssh.ServerConn, usage *usageStats) *connection {
	log.Infof("new connection: user = %s, remote = %v", conn.User(), conn.RemoteAddr())

	permitPortForwarding := false
	if _, ok := conn.Permissions.Extensions["permit-port-forwarding"]; ok {
		permitPortForwarding = true
	}

	administrator := false
	if _, ok := conn.Permissions.Extensions["administrator"]; ok {
		administrator = true
	}

	return &connection{
		id:                   ksuid.New().String(),
		gateway:              gateway,
		conn:                 conn,
		user:                 conn.User(),
		remoteAddr:           conn.RemoteAddr(),
		localAddr:            conn.LocalAddr(),
		services:             make(map[string]map[uint16]bool),
		usage:                usage,
		permitPortForwarding: permitPortForwarding,
		administrator:        administrator,
	}
}

// close the ssh connection
func (self *connection) close() {
	defer self.waitGroup.Wait()
	self.closeOnce.Do(func() {
		self.gateway.deleteConnection(self)

		if err := self.conn.Close(); err != nil {
			log.Debugf("failed to close connection: %s", err)
		}

		log.Infof("connection closed: user = %s, remote = %v", self.user, self.remoteAddr)
	})
}

// tunnels within a ssh connection
func (self *connection) listTunnels() []*tunnel {
	self.lock.Lock()
	defer self.lock.Unlock()

	tunnels := make([]*tunnel, len(self.tunnels))
	copy(tunnels, self.tunnels)
	return tunnels
}

func (self *connection) addTunnel(t *tunnel) {
	self.lock.Lock()
	defer self.lock.Unlock()

	self.tunnels = append([]*tunnel{t}, self.tunnels...)
}

func (self *connection) deleteTunnel(t *tunnel) {
	self.lock.Lock()
	defer self.lock.Unlock()

	// filter the list of channels
	tunnels := make([]*tunnel, 0, len(self.tunnels))
	for _, tunnel := range self.tunnels {
		if tunnel != t {
			tunnels = append(tunnels, tunnel)
		}
	}
	self.tunnels = tunnels
	self.tunnelsClosed += 1
}

// returns the last used time of the connection
func (self *connection) getUsed() time.Time {
	self.lock.Lock()
	defer self.lock.Unlock()

	return self.usage.used
}

// returns the list of services this connection advertises
func (self *connection) listServices() map[string][]uint16 {
	self.lock.Lock()
	defer self.lock.Unlock()

	services := make(map[string][]uint16)
	for host, ports := range self.services {
		for port, ok := range ports {
			if ok {
				services[host] = append(services[host], port)
			}
		}
	}

	return services
}

func (self *connection) reportStatus(status json.RawMessage) error {
	if _, err := self.gateway.database.PutUser(self.user, func(model *db.User) error {
		model.Status = db.Status(status)
		return nil
	}); err != nil {
		log.Errorf("failed to save user in database: %s", err)
		return err
	}
	return nil
}

type connectionStatus struct {
	ID string `json:"id,omitempty"`

	Created time.Time `json:"created,omitempty"`
	Used    time.Time `json:"used,omitempty"`

	User                 string `json:"user,omitempty"`
	Administrator        bool   `json:"administrator,omitempty"`
	PermitPortForwarding bool   `json:"permit_port_forwarding,omitempty"`
	Address              string `json:"address,omitempty"`

	Tunnels       []*tunnelStatus `json:"tunnels,omitempty"`
	TunnelsClosed uint64          `json:"tunnels_closed,omitempty"`

	UpTime   uint64 `json:"up_time"`
	IdleTime uint64 `json:"idle_time"`

	BytesRead    uint64 `json:"bytes_read"`
	BytesWritten uint64 `json:"bytes_written"`

	Services map[string][]uint16 `json:"tunnels_closed,omitempty"`
}

func (self *connection) gatherStatus() *connectionStatus {
	self.lock.Lock()
	defer self.lock.Unlock()

	// services
	services := make(map[string][]uint16)
	for host, ports := range self.services {
		for port, ok := range ports {
			if ok {
				services[host] = append(services[host], port)
			}
		}
	}

	tunnels := make([]*tunnelStatus, 0, len(self.tunnels))
	for _, tunnel := range self.tunnels {
		tunnels = append(tunnels, tunnel.gatherStatus())
	}

	return &connectionStatus{
		ID:                   self.id,
		User:                 self.user,
		Administrator:        self.administrator,
		PermitPortForwarding: self.permitPortForwarding,
		Address:              self.remoteAddr.String(),
		Tunnels:              tunnels,
		TunnelsClosed:        self.tunnelsClosed,
		Created:              self.usage.created,
		Used:                 self.usage.used,
		UpTime:               uint64(time.Since(self.usage.created).Seconds()),
		IdleTime:             uint64(time.Since(self.usage.used).Seconds()),
		BytesRead:            self.usage.bytesRead,
		BytesWritten:         self.usage.bytesWritten,
		Services:             services,
	}
}

func (self *connection) lookupService(host string, port uint16) bool {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, ok := self.services[host]; !ok {
		return false
	}

	return self.services[host][port]
}

func (self *connection) registerService(host string, port uint16) error {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, ok := self.services[host]; !ok {
		self.services[host] = make(map[uint16]bool)
	}
	if self.services[host][port] {
		return ErrServiceAlreadyRegistered
	}
	self.services[host][port] = true

	log.Debugf("registered service: user = %s, host = %s, port = %d", self.user, host, port)
	return nil
}

func (self *connection) deregisterService(host string, port uint16) error {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, ok := self.services[host]; ok {
		delete(self.services[host], port)
	}

	log.Debugf("deregistered service: user = %s, host = %s, port = %d", self.user, host, port)
	return nil
}

func (self *connection) handleRequests(requests <-chan *ssh.Request) {
	defer self.close()

	for request := range requests {
		self.handleRequest(request)
	}
}

func (self *connection) handleChannels(channels <-chan ssh.NewChannel) {
	defer self.close()

	for channel := range channels {
		self.handleChannel(channel)
	}
}

func (self *connection) handleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	ok := false
	switch request.Type {
	case "tcpip-forward":
		request, err := unmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Warningf("failed to decode request: %s", err)
			break
		}

		if request.Port == 0 {
			log.Warningf("requested forwarding port is not allowed: %d", request.Port)
			break
		}

		if err := self.registerService(request.Host, uint16(request.Port)); err != nil {
			log.Warningf("failed to register service in connection: %s", err)
			break
		}

		ok = true

	case "cancel-tcpip-forward":
		request, err := unmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Warningf("failed to decode request: %s", err)
			break
		}

		if err := self.deregisterService(request.Host, uint16(request.Port)); err != nil {
			log.Warningf("failed to register service in connection: %s", err)
			break
		}

		ok = true

	}

	if request.WantReply {
		if err := request.Reply(ok, nil); err != nil {
			log.Warningf("failed to reply to request: %s", err)
		}
	}
}

func (self *connection) handleChannel(newChannel ssh.NewChannel) {
	log.Debugf("new channel: type = %s, data = %v", newChannel.ChannelType(), newChannel.ExtraData())

	ok := false
	rejection := ssh.UnknownChannelType
	message := "unknown channel type"

	switch newChannel.ChannelType() {
	case "session":
		ok, rejection, message = self.handleSessionChannel(newChannel)
	case "direct-tcpip":
		ok, rejection, message = self.handleTunnelChannel(newChannel)
	}

	if ok {
		return
	}
	log.Debugf("channel rejected due to %d: %s", rejection, message)

	// reject the channel, by accepting it then immediately close
	// this is because Reject() leaks
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Warningf("failed to reject channel: %s", err)
		return
	}

	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		ssh.DiscardRequests(requests)
	}()

	if err := channel.Close(); err != nil {
		log.Warningf("failed to close rejected channel: %s", err)
		return
	}
}

func (self *connection) handleSessionChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason, string) {
	if len(newChannel.ExtraData()) > 0 {
		// do not accept extra data in connection channel request
		return false, ssh.Prohibited, "extra data not allowed"
	}

	// accept the channel
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Warningf("failed to accept channel: %s", err)
		return true, 0, ""
	}

	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		handleSession(self, channel, requests, newChannel.ChannelType(), newChannel.ExtraData())
	}()
	return true, 0, ""
}

func (self *connection) handleTunnelChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason, string) {

	data, err := unmarshalTunnelData(newChannel.ExtraData())
	if err != nil {
		return false, ssh.UnknownChannelType, "failed to decode extra data"
	}

	// look up connection by name
	otherConnection, host, port := self.gateway.lookupConnectionService(data.Host, uint16(data.Port))
	if otherConnection == nil {
		return false, ssh.ConnectionFailed, "service not found or not online"
	}

	// see if this connection is allowed
	if !self.permitPortForwarding {
		log.Warningf("no permission to port forward: user = %s", self.user)
		return false, ssh.Prohibited, "permission denied"
	}

	// found the service, attempt to open a channel
	data2 := &tunnelData{
		Host:          host,
		Port:          uint32(port),
		OriginAddress: data.OriginAddress,
		OriginPort:    data.OriginPort,
	}

	otherTunnel, err := otherConnection.openTunnel("forwarded-tcpip", marshalTunnelData(data2), map[string]interface{}{
		"origin": data.OriginAddress,
		"from": map[string]interface{}{
			"address": self.remoteAddr.String(),
			"user":    self.user,
		},
		"service": map[string]interface{}{
			"host": data.Host,
			"port": data.Port,
		},
	})
	if err != nil {
		return false, ssh.ConnectionFailed, "failed to connect"
	}
	defer func() {
		if otherTunnel != nil {
			otherTunnel.close()
		}
	}()

	// accept the channel
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Warningf("failed to accept channel: %s", err)
		return true, 0, ""
	}

	// cannot return false from this point on
	// also need to close the accepted channel
	defer func() {
		if channel != nil {
			if err := channel.Close(); err != nil {
				log.Warningf("failed to close accepted channel: %s", err)
			}
		}
	}()

	thisTunnel := newTunnel(self, channel, newChannel.ChannelType(), newChannel.ExtraData(), map[string]interface{}{
		"origin": data.OriginAddress,
		"to": map[string]interface{}{
			"user":    otherConnection.user,
			"address": otherConnection.remoteAddr.String(),
		},
		"service": map[string]interface{}{
			"host": data.Host,
			"port": data.Port,
		},
	})
	self.addTunnel(thisTunnel)

	// no failure
	self.waitGroup.Add(2)
	go func() {
		defer self.waitGroup.Done()
		thisTunnel.handleRequests(requests)
	}()
	go func(otherTunnel *tunnel) {
		defer self.waitGroup.Done()
		thisTunnel.handleTunnel(otherTunnel)
	}(otherTunnel)

	// do not close channel on exit
	channel = nil
	otherTunnel = nil
	return true, 0, ""
}

// open a channel from the server to the client side
func (self *connection) openTunnel(channelType string, extraData []byte, metadata map[string]interface{}) (*tunnel, error) {
	log.Debugf("opening channel: type = %s, data = %v", channelType, extraData)

	channel, requests, err := self.conn.OpenChannel(channelType, extraData)
	if err != nil {
		return nil, err
	}
	defer func() {
		if channel != nil {
			if err := channel.Close(); err != nil {
				log.Warningf("failed to close opened channel: %s", err)
			}
		}
	}()

	tunnel := newTunnel(self, channel, channelType, extraData, metadata)
	self.addTunnel(tunnel)

	// no failure
	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		tunnel.handleRequests(requests)
	}()

	// do not close channel on exit
	channel = nil
	return tunnel, nil
}
