package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/segmentio/ksuid"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
)

var (
	ErrServiceAlreadyRegistered = errors.New("gateway: service already registered")
	ErrServiceNotFound          = errors.New("gateway: service not found")
	ErrExtraDataNotAllowed      = errors.New("gateway: extra data not allowed")
	ErrUnknownChannelType       = errors.New("gateway: unknown channel type")
	ErrPermissionDenied         = errors.New("gateway: permission denied")
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
	tunnelsOpened        uint64
	tunnelsClosed        uint64
	services             map[string]map[uint16]bool
	done                 chan struct{}
	lock                 sync.Mutex
	waitGroup            sync.WaitGroup
	usage                *usageStats
	permitPortForwarding bool
	administrator        bool
}

func handleConnection(gateway *gateway, conn *ssh.ServerConn, channels <-chan ssh.NewChannel, requests <-chan *ssh.Request, usage *usageStats) {
	permitPortForwarding := false
	if _, ok := conn.Permissions.Extensions["permit-port-forwarding"]; ok {
		permitPortForwarding = true
	}

	administrator := false
	if _, ok := conn.Permissions.Extensions["administrator"]; ok {
		administrator = true
	}

	self := &connection{
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
		done:                 make(chan struct{}),
	}
	log.Infof("%s: new connection", self)
	self.gateway.addConnection(self)

	// handle requests and channels on this connection
	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		defer func() {
			self.gateway.deleteConnection(self)
			if err := self.conn.Close(); err != nil {
				log.Warningf("%s: failed to close connection: %s", self, err)
			}
			log.Infof("%s: connection closed", self)
		}()

		for {
			select {
			case <-self.done:
				return
			case request, ok := <-requests:
				if !ok {
					return
				}
				if err := self.handleRequest(request); err != nil {
					log.Warningf("%s: failed to handle request: %s", self, err)
					return
				}
			case channel, ok := <-channels:
				if !ok {
					return
				}
				if err := self.handleChannel(channel); err != nil {
					log.Warningf("%s: failed to handle channel: %s", self, err)
					return
				}
			}
		}
	}()
}

func (self *connection) String() string {
	return fmt.Sprintf("connection(id=%q, remote=%q, user=%q)", self.id, self.remoteAddr, self.user)
}

// close the ssh connection
func (self *connection) close() {
	defer self.waitGroup.Wait()
	close(self.done)
	log.Debugf("%s: connection closed", self)
}

// tunnels within a ssh connection
// func (self *connection) listTunnels() []*tunnel {
// 	self.lock.Lock()
// 	defer self.lock.Unlock()

// 	tunnels := make([]*tunnel, len(self.tunnels))
// 	copy(tunnels, self.tunnels)
// 	return tunnels
// }

func (self *connection) addTunnel(t *tunnel) {
	self.lock.Lock()
	defer self.lock.Unlock()

	self.tunnels = append([]*tunnel{t}, self.tunnels...)
	self.tunnelsOpened += 1
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
func (self *connection) getUsedAt() time.Time {
	self.lock.Lock()
	defer self.lock.Unlock()

	return self.usage.usedAt
}

// returns the list of services this connection advertises
// func (self *connection) listServices() map[string][]uint16 {
// 	self.lock.Lock()
// 	defer self.lock.Unlock()

// 	services := make(map[string][]uint16)
// 	for host, ports := range self.services {
// 		for port, ok := range ports {
// 			if ok {
// 				services[host] = append(services[host], port)
// 			}
// 		}
// 	}

// 	return services
// }

func (self *connection) reportStatus(status json.RawMessage) error {
	if _, err := self.gateway.database.PutUser(self.user, func(model *db.User) error {
		model.Status = db.Status(status)
		return nil
	}); err != nil {
		log.Errorf("%s: failed to save user in database: %s", self, err)
		return err
	}
	return nil
}

func (self *connection) reportScreenshot(screenshot []byte) error {
	if _, err := self.gateway.database.PutUser(self.user, func(model *db.User) error {
		model.Screenshot = screenshot
		return nil
	}); err != nil {
		log.Errorf("%s: failed to save user in database: %s", self, err)
		return err
	}
	return nil
}

type connectionStatus struct {
	ID string `json:"id,omitempty"`

	CreatedAt time.Time `json:"createdAt,omitempty"`
	UsedAt    time.Time `json:"usedAt,omitempty"`

	User                 string `json:"user,omitempty"`
	Administrator        bool   `json:"administrator,omitempty"`
	PermitPortForwarding bool   `json:"permitPortForwarding,omitempty"`
	Address              string `json:"address,omitempty"`

	Tunnels       []*tunnelStatus `json:"tunnels,omitempty"`
	TunnelsActive uint64          `json:"tunnelsActive,omitempty"`
	TunnelsOpened uint64          `json:"tunnelsOpened,omitempty"`
	TunnelsClosed uint64          `json:"tunnelsClosed,omitempty"`

	UpTime   uint64 `json:"upTime"`
	IdleTime uint64 `json:"idleTime"`

	BytesRead    uint64 `json:"bytesRead"`
	BytesWritten uint64 `json:"bytesWritten"`

	Services map[string][]uint16 `json:"services,omitempty"`
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
		TunnelsActive:        uint64(len(tunnels)),
		TunnelsOpened:        self.tunnelsOpened,
		TunnelsClosed:        self.tunnelsClosed,
		CreatedAt:            self.usage.createdAt,
		UsedAt:               self.usage.usedAt,
		UpTime:               uint64(time.Since(self.usage.createdAt).Seconds()),
		IdleTime:             uint64(time.Since(self.usage.usedAt).Seconds()),
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

	log.Debugf("%s: registered service: host = %s, port = %d", self, host, port)
	return nil
}

func (self *connection) deregisterService(host string, port uint16) error {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, ok := self.services[host]; ok {
		delete(self.services[host], port)
	}

	log.Debugf("%s: deregistered service: host = %s, port = %d", self, host, port)
	return nil
}

func (self *connection) handleRequest(request *ssh.Request) error {
	// log.Debugf("request received: type = %s, want_reply = %v, payload = %d", request.Type, request.WantReply, len(request.Payload))

	ok := false
	switch request.Type {
	case "tcpip-forward":
		request, err := unmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Errorf("%s: failed to decode request: %s", self, err)
			return err
		}

		if request.Port == 0 {
			log.Errorf("%s: requested forwarding port is not allowed: %d", self, request.Port)
			return err
		}

		if err := self.registerService(request.Host, uint16(request.Port)); err != nil {
			log.Errorf("%s: failed to register service in connection: %s", self, err)
			return err
		}

		ok = true
	case "cancel-tcpip-forward":
		request, err := unmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Errorf("%s: failed to decode request: %s", self, err)
			return err
		}

		if err := self.deregisterService(request.Host, uint16(request.Port)); err != nil {
			log.Errorf("%s: failed to register service in connection: %s", self, err)
			return err
		}

		ok = true
	}
	if request.WantReply {
		if err := request.Reply(ok, nil); err != nil {
			log.Errorf("%s: failed to reply to request: %s", self, err)
			return err
		}
	}
	return nil
}

func (self *connection) handleChannel(newChannel ssh.NewChannel) error {
	log.Debugf("%s: new channel: type = %s, data = %v", self, newChannel.ChannelType(), newChannel.ExtraData())

	ok := false
	rejection := ssh.UnknownChannelType
	message := "unknown channel type"
	err := ErrUnknownChannelType

	switch newChannel.ChannelType() {
	case "session":
		ok, rejection, message, err = self.handleSessionChannel(newChannel)
	case "direct-tcpip":
		ok, rejection, message, err = self.handleTunnelChannel(newChannel)
	}
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	log.Debugf("%s: channel rejected due to %d: %s", self, rejection, message)

	// reject the channel, by accepting it then immediately close
	// this is because Reject() leaks
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Errorf("%s: failed to reject channel: %s", self, err)
		return err
	}

	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		ssh.DiscardRequests(requests)
	}()

	if err := channel.Close(); err != nil {
		log.Errorf("%s: failed to close rejected channel: %s", self, err)
		return err
	}
	return nil
}

func (self *connection) handleSessionChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason, string, error) {
	if len(newChannel.ExtraData()) > 0 {
		// do not accept extra data in connection channel request
		return false, ssh.Prohibited, "extra data not allowed", ErrExtraDataNotAllowed
	}

	// accept the channel
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Errorf("%s: failed to accept channel: %s", self, err)
		return true, 0, "", err
	}

	// cannot return false from this point on
	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		handleSession(self, channel, requests, newChannel.ChannelType(), newChannel.ExtraData())
	}()
	return true, 0, "", nil
}

func (self *connection) handleTunnelChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason, string, error) {
	data, err := unmarshalTunnelData(newChannel.ExtraData())
	if err != nil {
		return false, ssh.UnknownChannelType, "failed to decode extra data", err
	}

	// see if this connection is allowed
	if !self.permitPortForwarding {
		log.Errorf("%s: no permission to port forward", self)
		return false, ssh.Prohibited, "permission denied", ErrPermissionDenied
	}

	// look up connection by name
	otherConnection, host, port := self.gateway.lookupConnectionService(data.Host, uint16(data.Port))
	if otherConnection == nil {
		return false, ssh.ConnectionFailed, "service not found or not online", nil
	}

	// accept the channel
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Errorf("%s: failed to accept channel: %s", self, err)
		return true, 0, "", err
	}

	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()

		// first need to track the opened channel
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
		self.waitGroup.Add(1)
		go func() {
			defer self.waitGroup.Done()
			thisTunnel.handleRequests(requests)
		}()
		defer thisTunnel.close()

		// attempt to open a channel, this can block
		otherTunnel, err := otherConnection.openTunnel("forwarded-tcpip", marshalTunnelData(&tunnelData{
			Host:          host,
			Port:          uint32(port),
			OriginAddress: data.OriginAddress,
			OriginPort:    data.OriginPort,
		}), map[string]interface{}{
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
			log.Warningf("%s: failed to open tunnel to %s: %s", self, otherConnection, err)
			return
		}
		defer otherTunnel.close()

		thisTunnel.handleTunnel(otherTunnel)
	}()
	return true, 0, "", nil
}

// open a channel from the server to the client side
func (self *connection) openTunnel(channelType string, extraData []byte, metadata map[string]interface{}) (*tunnel, error) {
	log.Debugf("%s: opening channel: type = %s, data = %v", self, channelType, extraData)

	channel, requests, err := self.conn.OpenChannel(channelType, extraData)
	if err != nil {
		return nil, err
	}

	// no failure
	tunnel := newTunnel(self, channel, channelType, extraData, metadata)
	self.waitGroup.Add(1)
	go func() {
		defer self.waitGroup.Done()
		tunnel.handleRequests(requests)
	}()
	return tunnel, nil
}
