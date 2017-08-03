package gateway

import (
	"errors"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ErrServiceAlreadyRegistered = errors.New("gatewaysshd: service already registered")
	ErrServiceNotFound          = errors.New("gatewaysshd: service not found")
)

type Session struct {
	gateway        *Gateway
	connection     *ssh.ServerConn
	user           string
	remoteAddr     net.Addr
	localAddr      net.Addr
	channels       []*Channel
	channelsClosed uint64
	services       map[string]map[uint16]bool
	lock           *sync.Mutex
	active         bool
	closeOnce      sync.Once
	created        time.Time
	used           time.Time
	bytesRead      uint64
	bytesWritten   uint64
}

func NewSession(gateway *Gateway, connection *ssh.ServerConn) (*Session, error) {
	log.Infof("new session: user = %s, remote = %v", connection.User(), connection.RemoteAddr())

	return &Session{
		gateway:        gateway,
		connection:     connection,
		user:           connection.User(),
		remoteAddr:     connection.RemoteAddr(),
		localAddr:      connection.LocalAddr(),
		services:       make(map[string]map[uint16]bool),
		lock:           &sync.Mutex{},
		active:         true,
		created:        time.Now(),
		used:           time.Now(),
		channelsClosed: 0,
		bytesRead:      0,
		bytesWritten:   0,
	}, nil
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.active = false
		s.Gateway().DeleteSession(s)

		for _, channel := range s.Channels() {
			channel.Close()
		}

		if err := s.connection.Close(); err != nil {
			log.Debugf("failed to close session: %s", err)
		}

		log.Infof("session closed: user = %s, remote = %v, status = %v", s.User(), s.RemoteAddr(), s.Status())
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

func (s *Session) Used() time.Time {
	s.lock.Lock()
	defer s.lock.Unlock()

	used := s.used
	for _, channel := range s.channels {
		u := channel.Used()
		if u.After(used) {
			used = u
		}
	}
	return used
}

func (s *Session) BytesRead() uint64 {
	s.lock.Lock()
	defer s.lock.Unlock()

	bytesRead := s.bytesRead
	for _, channel := range s.channels {
		bytesRead += channel.BytesRead()
	}
	return bytesRead
}

func (s *Session) BytesWritten() uint64 {
	s.lock.Lock()
	defer s.lock.Unlock()

	bytesWritten := s.bytesWritten
	for _, channel := range s.channels {
		bytesWritten += channel.BytesWritten()
	}
	return bytesWritten
}

func (s *Session) ChannelsClosed() uint64 {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.channelsClosed
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

	// keep used time
	used := c.Used()
	if used.After(s.used) {
		s.used = used
	}

	// keep stats
	s.bytesRead += c.BytesRead()
	s.bytesWritten += c.BytesWritten()

	s.channelsClosed += 1
}

func (s *Session) Channels() []*Channel {
	s.lock.Lock()
	defer s.lock.Unlock()

	channels := make([]*Channel, len(s.channels))
	copy(channels, s.channels)
	return channels
}

func (s *Session) ChannelsCount() int {
	s.lock.Lock()
	defer s.lock.Unlock()

	return len(s.channels)
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

func (s *Session) Status() map[string]interface{} {
	s.lock.Lock()
	defer s.lock.Unlock()

	// stats
	bytesRead := s.bytesRead
	bytesWritten := s.bytesWritten
	used := s.used
	for _, channel := range s.channels {
		bytesRead += channel.BytesRead()
		bytesWritten += channel.BytesWritten()
		u := channel.Used()
		if u.After(used) {
			used = u
		}
	}

	// services
	services := make(map[string][]uint16)
	for host, ports := range s.services {
		for port, ok := range ports {
			if ok {
				services[host] = append(services[host], port)
			}
		}
	}

	return map[string]interface{}{
		"user":            s.user,
		"address":         s.remoteAddr.String(),
		"channels_count":  len(s.channels),
		"channels_closed": s.channelsClosed,
		"created":         s.created.Unix(),
		"used":            used.Unix(),
		"uptime":          uint64(time.Since(s.created).Seconds()),
		"idletime":        uint64(time.Since(used).Seconds()),
		"bytes_read":      bytesRead,
		"bytes_written":   bytesWritten,
		"services":        services,
	}
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

	log.Infof("registered service: user = %s, host = %s, port = %d", s.user, host, port)
	return nil
}

func (s *Session) DeregisterService(host string, port uint16) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.services[host]; ok {
		delete(s.services[host], port)
	}

	log.Infof("deregistered service: user = %s, host = %s, port = %d", s.user, host, port)
	return nil
}

func (s *Session) HandleRequests(requests <-chan *ssh.Request) {
	defer s.Close()

	for request := range requests {
		s.used = time.Now()
		go s.HandleRequest(request)
	}
}

func (s *Session) HandleChannels(channels <-chan ssh.NewChannel) {
	defer s.Close()

	for channel := range channels {
		s.used = time.Now()
		go s.HandleChannel(channel)
	}
}

func (s *Session) HandleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	ok := false
	switch request.Type {
	case "tcpip-forward":
		request, err := UnmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Errorf("failed to decode request: %s", err)
			break
		}

		if request.Port == 0 {
			log.Errorf("requested forwarding port is not allowed: %d", request.Port)
			break
		}

		if err := s.RegisterService(request.Host, uint16(request.Port)); err != nil {
			log.Errorf("failed to register service in session: %s", err)
			break
		}

		ok = true

	case "cancel-tcpip-forward":
		request, err := UnmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Errorf("failed to decode request: %s", err)
			break
		}

		if err := s.DeregisterService(request.Host, uint16(request.Port)); err != nil {
			log.Errorf("failed to register service in session: %s", err)
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

func (s *Session) HandleChannel(newChannel ssh.NewChannel) {
	log.Debugf("new channel: type = %s, data = %v", newChannel.ChannelType(), newChannel.ExtraData())

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
			log.Warningf("failed to reject channel: %s", err)
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
				log.Warningf("failed to close accepted channel: %s", err)
			}
		}
	}()

	c, err := NewChannel(s, channel, newChannel.ChannelType(), newChannel.ExtraData())
	if err != nil {
		log.Errorf("failed to create accepted channel: %s", err)
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

	// see if this connection is allowed
	if _, ok := s.connection.Permissions.Extensions["permit-port-forwarding"]; !ok {
		log.Warningf("no permission to port forward: user = %s", s.user)
		return false, ssh.Prohibited
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
	// also need to close the accepted channel
	defer func() {
		if channel != nil {
			if err := channel.Close(); err != nil {
				log.Warningf("failed to close accepted channel: %s", err)
			}
		}
	}()

	c, err := NewChannel(s, channel, newChannel.ChannelType(), newChannel.ExtraData())
	if err != nil {
		log.Errorf("failed to create accepted channel: %s", err)
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
	log.Debugf("opening channel: type = %s, data = %v", channelType, extraData)

	channel, requests, err := s.connection.OpenChannel(channelType, extraData)
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
