package gateway

import (
	"errors"
	"strings"
	"sync"

	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

var (
	ErrServiceAlreadyRegistered = errors.New("gatewaysshd: service already registered")
)

type Session struct {
	gateway    *Gateway
	connection *ssh.ServerConn
	user       string
	services   map[string]map[uint16]bool
	lock       *sync.Mutex
	active     bool
	close      sync.Once
}

func NewSession(gateway *Gateway, connection *ssh.ServerConn) (*Session, error) {
	return &Session{
		gateway:    gateway,
		connection: connection,
		user:       connection.User(),
		services:   make(map[string]map[uint16]bool),
		lock:       &sync.Mutex{},
		active:     true,
	}, nil
}

func (s *Session) User() string {
	return s.user
}

func (s *Session) LookupService(service string, port uint16) bool {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[service]; !ok {
		return false
	}

	return s.services[service][port]
}

func (s *Session) RegisterService(payload []byte) error {
	request, err := UnmarshalForwardRequest(payload)
	if err != nil {
		return err
	}

	host := request.Host
	port := uint16(request.Port)

	// do the registration
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[host]; !ok {
		s.services[host] = make(map[uint16]bool)
	}
	if s.services[host][port] {
		return ErrServiceAlreadyRegistered
	}
	s.services[host][port] = true

	glog.Infof("gatewaysshd: registered service in session: host = %s, port = %d", host, port)
	return nil
}

func (s *Session) DeregisterService(payload []byte) error {
	request, err := UnmarshalForwardRequest(payload)
	if err != nil {
		return err
	}

	host := request.Host
	port := uint16(request.Port)

	// do the deregistration
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[host]; ok {
		delete(s.services[host], port)
	}

	glog.Infof("gatewaysshd: deregistered service in session: host = %s, port = %d", host, port)
	return nil
}

func (s *Session) HandleRequest(request *ssh.Request) {
	glog.Infof("gatewaysshd: request received in session: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)
	ok := true

	switch request.Type {
	case "tcpip-forward":
		if err := s.RegisterService(request.Payload); err != nil {
			glog.Errorf("gatewaysshd: failed to register service in session: %s", err)
			ok = false
		}
	case "cancel-tcpip-forward":
		if err := s.DeregisterService(request.Payload); err != nil {
			glog.Errorf("gatewaysshd: failed to register service in session: %s", err)
			ok = false
		}
	default:
		// for other requests, ignore or reject them all
		ok = false
	}

	if request.WantReply {
		if err := request.Reply(ok, nil); err != nil {
			glog.Warningf("gatewaysshd: failed to reply to request: %s", err)
		}
	}
}

func (s *Session) HandleRequests(requests <-chan *ssh.Request) {
	defer s.Close()

	for request := range requests {
		go s.HandleRequest(request)
	}
}

func (s *Session) HandleChannel(channel ssh.NewChannel) {

	rejection := ssh.ResourceShortage
	defer func() {
		if channel != nil {
			// reject
			if err := channel.Reject(rejection, ""); err != nil {
				glog.Warningf("gatewaysshd: failed to reject channel: %s", err)
			}
		}
	}()

	var ch2 *Channel
	defer func() {
		if ch2 != nil {
			ch2.Close()
		}
	}()

	channelType := channel.ChannelType()
	extraData := channel.ExtraData()

	switch channelType {
	case "session":
		if len(extraData) > 0 {
			rejection = ssh.UnknownChannelType
			return
		}

	case "direct-tcpip":
		data, err := UnmarshalTunnelData(extraData)
		if err != nil {
			rejection = ssh.UnknownChannelType
			return
		}

		port := uint16(data.Port)
		host := strings.Split(data.Host, ".")
		user := host[len(host)-1]
		service := strings.Join(host[:len(host)-1], ".")
		glog.Infof("gatewaysshd: looking up: user = %s, service = %s, port = %d", user, service, port)

		// look up session by name
		session := s.gateway.LookupSessionForService(user, service, port)
		if session == nil {
			rejection = ssh.ConnectionFailed
			return
		}

		// found the service
		data.Host = service
		ch2, err = session.OpenChannel("forwarded-tcpip", MarshalTunnelData(data))
		if err != nil {
			rejection = ssh.ConnectionFailed
			return
		}

	default:
		rejection = ssh.UnknownChannelType
		return
	}

	c, requests, err := channel.Accept()

	// can no longer reject from this point on
	channel = nil

	if err != nil {
		return
	}

	defer func() {
		if c != nil {
			if err := c.Close(); err != nil {
				glog.Warningf("gatewaysshd: failed to close channel: %s", err)
			}
		}
	}()

	ch, err := NewChannel(s, c, channelType, extraData)
	if err != nil {
		glog.Errorf("gatewaysshd: failed to create channel: %s", err)
		return
	}
	c = nil

	go ch.HandleRequests(requests)
	if ch2 != nil {
		go ch.HandleTunnel(ch2)
		ch2 = nil
	}
}

func (s *Session) HandleChannels(channels <-chan ssh.NewChannel) {
	defer s.Close()

	for channel := range channels {
		go s.HandleChannel(channel)
	}
}

// open a channel from the server to the client side
func (s *Session) OpenChannel(channelType string, extraData []byte) (*Channel, error) {
	c, requests, err := s.connection.OpenChannel(channelType, extraData)
	if err != nil {
		return nil, err
	}

	ch, err := NewChannel(s, c, channelType, extraData)
	if err != nil {
		return nil, err
	}

	go ch.HandleRequests(requests)

	return ch, nil
}

func (s *Session) Close() {
	s.close.Do(func() {
		s.active = false
		s.gateway.DeleteSession(s)
		glog.Infof("gatewaysshd: session closed: %s", s.user)
	})
}
