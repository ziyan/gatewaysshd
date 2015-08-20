package gateway

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

var (
	ErrServiceAlreadyRegistered = errors.New("gatewaysshd: service already registered")
	ErrServiceNotFound          = errors.New("gatewaysshd: service not found")
)

type Session struct {
	gateway    *Gateway
	connection *ssh.ServerConn
	user       string
	remoteAddr net.Addr
	localAddr  net.Addr
	channels   []*Channel
	services   map[string]map[uint16]bool
	lock       *sync.Mutex
	active     bool
	closeOnce  sync.Once
	created    time.Time
}

func NewSession(gateway *Gateway, connection *ssh.ServerConn) (*Session, error) {
	return &Session{
		gateway:    gateway,
		connection: connection,
		user:       connection.User(),
		remoteAddr: connection.RemoteAddr(),
		localAddr:  connection.LocalAddr(),
		services:   make(map[string]map[uint16]bool),
		lock:       &sync.Mutex{},
		active:     true,
		created:    time.Now(),
	}, nil
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.active = false
		s.Gateway().DeleteSession(s)
		glog.V(1).Infof("session closed: %s", s.user)
	})
}

func (s *Session) Gateway() *Gateway {
	return s.gateway
}

func (s *Session) User() string {
	return s.user
}

func (s *Session) RemoteAddr() net.Addr {
	return s.remoteAddr
}

func (s *Session) LocalAddr() net.Addr {
	return s.remoteAddr
}

func (s *Session) Created() time.Time {
	return s.created
}

func (s *Session) AddChannel(c *Channel) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.channels = append([]*Channel{c}, s.channels...)
}

func (s *Session) DeleteChannel(c *Channel) {
	s.lock.Lock()
	defer s.lock.Unlock()

	// filter the list of channels
	channels := make([]*Channel, 0, len(s.channels))
	for _, channel := range s.channels {
		if channel != c {
			channels = append(channels, channel)
		}
	}
	s.channels = channels
}

func (s *Session) Services() map[string][]uint16 {
	s.lock.Lock()
	defer s.lock.Unlock()

	services := make(map[string][]uint16)
	for host, ports := range s.services {
		for port, ok := range ports {
			if ok {
				services[host] = append(services[host], port)
			}
		}
	}

	return services
}

func (s *Session) LookupService(host string, port uint16) bool {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[host]; !ok {
		return false
	}

	return s.services[host][port]
}

func (s *Session) RegisterService(host string, port uint16) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[host]; !ok {
		s.services[host] = make(map[uint16]bool)
	}
	if s.services[host][port] {
		return ErrServiceAlreadyRegistered
	}
	s.services[host][port] = true

	glog.V(1).Infof("registered service: user = %s, host = %s, port = %d", s.user, host, port)
	return nil
}

func (s *Session) DeregisterService(host string, port uint16) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[host]; ok {
		delete(s.services[host], port)
	}

	glog.V(1).Infof("deregistered service: user = %s, host = %s, port = %d", s.user, host, port)
	return nil
}

func (s *Session) HandleRequests(requests <-chan *ssh.Request) {
	defer s.Close()

	for request := range requests {
		go s.HandleRequest(request)
	}
}

func (s *Session) HandleChannels(channels <-chan ssh.NewChannel) {
	defer s.Close()

	for channel := range channels {
		go s.HandleChannel(channel)
	}
}

func (s *Session) HandleRequest(request *ssh.Request) {
	glog.V(2).Infof("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)
	ok := false
	var reply []byte

	switch request.Type {
	case "tcpip-forward":
		request, err := UnmarshalForwardRequest(request.Payload)
		if err != nil {
			glog.Errorf("failed to decode request: %s", err)
			break
		}

		if err := s.RegisterService(request.Host, uint16(request.Port)); err != nil {
			glog.Errorf("failed to register service in session: %s", err)
			break
		}

		reply = MarshalForwardReply(&ForwardReply{Port: request.Port})
		ok = true
	case "cancel-tcpip-forward":
		request, err := UnmarshalForwardRequest(request.Payload)
		if err != nil {
			glog.Errorf("failed to decode request: %s", err)
			break
		}

		if err := s.DeregisterService(request.Host, uint16(request.Port)); err != nil {
			glog.Errorf("failed to register service in session: %s", err)
			break
		}

		ok = true
	}

	if request.WantReply {
		if err := request.Reply(ok, reply); err != nil {
			glog.Warningf("failed to reply to request: %s", err)
		}
	}
}

func (s *Session) HandleChannel(newChannel ssh.NewChannel) {
	glog.V(2).Infof("new channel: type = %s, data = %v", newChannel.ChannelType(), newChannel.ExtraData())

	ok := false
	rejection := ssh.UnknownChannelType

	switch newChannel.ChannelType() {
	case "session":
		ok, rejection = s.HandleSessionChannel(newChannel)
	case "direct-tcpip":
		ok, rejection = s.HandleDirectChannel(newChannel)
	}

	if !ok {
		// reject the channel
		if err := newChannel.Reject(rejection, ""); err != nil {
			glog.Warningf("failed to reject channel: %s", err)
		}
	}
}

func (s *Session) HandleSessionChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason) {
	if len(newChannel.ExtraData()) > 0 {
		// do not accept extra data in session channel request
		return false, ssh.Prohibited
	}

	// accept the channel
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return false, ssh.ResourceShortage
	}

	// cannot return false from this point on
	// also need to accepted close the channel
	defer func() {
		if channel != nil {
			if err := channel.Close(); err != nil {
				glog.Warningf("failed to close accepted channel: %s", err)
			}
		}
	}()

	c, err := NewChannel(s, channel, newChannel.ChannelType(), newChannel.ExtraData())
	if err != nil {
		glog.Errorf("failed to create accepted channel: %s", err)
		return true, 0
	}
	s.AddChannel(c)

	// no failure
	go c.HandleRequests(requests)
	go c.HandleSessionChannel()

	// do not close channel on exit
	channel = nil
	return true, 0
}

func (s *Session) HandleDirectChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason) {

	data, err := UnmarshalTunnelData(newChannel.ExtraData())
	if err != nil {
		return false, ssh.UnknownChannelType
	}

	// look up session by name
	session, host, port := s.Gateway().LookupSessionService(data.Host, uint16(data.Port))
	if session == nil {
		return false, ssh.ConnectionFailed
	}

	// found the service, attempt to open a channel
	data.Host = host
	data.Port = uint32(port)

	c2, err := session.OpenChannel("forwarded-tcpip", MarshalTunnelData(data))
	if err != nil {
		return false, ssh.ConnectionFailed
	}
	defer func() {
		if c2 != nil {
			c2.Close()
		}
	}()

	// accept the channel
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return false, ssh.ResourceShortage
	}

	// cannot return false from this point on
	// also need to accepted close the channel
	defer func() {
		if channel != nil {
			if err := channel.Close(); err != nil {
				glog.Warningf("failed to close accepted channel: %s", err)
			}
		}
	}()

	c, err := NewChannel(s, channel, newChannel.ChannelType(), newChannel.ExtraData())
	if err != nil {
		glog.Errorf("failed to create accepted channel: %s", err)
		return true, 0
	}
	s.AddChannel(c)

	// no failure
	go c.HandleRequests(requests)
	go c.HandleTunnelChannel(c2)

	// do not close channel on exit
	channel = nil
	c2 = nil
	return true, 0
}

// open a channel from the server to the client side
func (s *Session) OpenChannel(channelType string, extraData []byte) (*Channel, error) {
	glog.V(2).Infof("opening channel: type = %s, data = %v", channelType, extraData)

	channel, requests, err := s.connection.OpenChannel(channelType, extraData)
	if err != nil {
		return nil, err
	}
	defer func() {
		if channel != nil {
			if err := channel.Close(); err != nil {
				glog.Warningf("failed to close opened channel: %s", err)
			}
		}
	}()

	c, err := NewChannel(s, channel, channelType, extraData)
	if err != nil {
		return nil, err
	}
	s.AddChannel(c)

	// no failure
	go c.HandleRequests(requests)

	// do not close channel on exit
	channel = nil
	return c, nil
}
