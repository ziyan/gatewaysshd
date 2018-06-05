package gateway

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ErrServiceAlreadyRegistered = errors.New("gatewaysshd: service already registered")
	ErrServiceNotFound          = errors.New("gatewaysshd: service not found")
)

type Connection struct {
	gateway        *Gateway
	conn           *ssh.ServerConn
	user           string
	remoteAddr     net.Addr
	localAddr      net.Addr
	sessions       []*Session
	sessionsClosed uint64
	tunnels        []*Tunnel
	tunnelsClosed  uint64
	services       map[string]map[uint16]bool
	lock           *sync.Mutex
	closeOnce      sync.Once
	created        time.Time
	used           time.Time
	bytesRead      uint64
	bytesWritten   uint64
	admin          bool
	status         json.RawMessage
	location       map[string]interface{}
}

func newConnection(gateway *Gateway, conn *ssh.ServerConn) (*Connection, error) {
	log.Infof("new connection: user = %s, remote = %v", conn.User(), conn.RemoteAddr())

	admin := true
	if _, ok := conn.Permissions.Extensions["permit-port-forwarding"]; !ok {
		admin = false
	}

	return &Connection{
		gateway:    gateway,
		conn:       conn,
		user:       conn.User(),
		remoteAddr: conn.RemoteAddr(),
		localAddr:  conn.LocalAddr(),
		services:   make(map[string]map[uint16]bool),
		lock:       &sync.Mutex{},
		created:    time.Now(),
		used:       time.Now(),
		admin:      admin,
		location:   lookupLocation(gateway.geoipDatabase, conn.RemoteAddr().(*net.TCPAddr).IP),
	}, nil
}

func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		c.gateway.deleteConnection(c)

		for _, session := range c.Sessions() {
			session.Close()
		}

		for _, tunnel := range c.Tunnels() {
			tunnel.Close()
		}

		if err := c.conn.Close(); err != nil {
			log.Debugf("failed to close connection: %s", err)
		}

		log.Infof("connection closed: user = %s, remote = %v, read = %d, written = %d", c.user, c.remoteAddr, c.bytesRead, c.bytesWritten)
	})
}

func (c *Connection) Sessions() []*Session {
	c.lock.Lock()
	defer c.lock.Unlock()

	sessions := make([]*Session, len(c.sessions))
	copy(sessions, c.sessions)
	return sessions
}

func (c *Connection) addSession(s *Session) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.sessions = append([]*Session{s}, c.sessions...)
}

func (c *Connection) deleteSession(s *Session) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// filter the list of channels
	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		if session != s {
			sessions = append(sessions, session)
		}
	}
	c.sessions = sessions

	// keep stats
	if s.used.After(c.used) {
		c.used = s.used
	}
	c.bytesRead += s.bytesRead
	c.bytesWritten += s.bytesWritten
	c.sessionsClosed += 1
}

func (c *Connection) Tunnels() []*Tunnel {
	c.lock.Lock()
	defer c.lock.Unlock()

	tunnels := make([]*Tunnel, len(c.tunnels))
	copy(tunnels, c.tunnels)
	return tunnels
}

func (c *Connection) addTunnel(t *Tunnel) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.tunnels = append([]*Tunnel{t}, c.tunnels...)
}

func (c *Connection) deleteTunnel(t *Tunnel) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// filter the list of channels
	tunnels := make([]*Tunnel, 0, len(c.tunnels))
	for _, tunnel := range c.tunnels {
		if tunnel != t {
			tunnels = append(tunnels, tunnel)
		}
	}
	c.tunnels = tunnels

	// keep stats
	if t.used.After(c.used) {
		c.used = t.used
	}
	c.bytesRead += t.bytesRead
	c.bytesWritten += t.bytesWritten
	c.tunnelsClosed += 1
}

func (c *Connection) Used() time.Time {
	c.lock.Lock()
	defer c.lock.Unlock()

	used := c.used

	for _, tunnel := range c.tunnels {
		if tunnel.used.After(used) {
			used = tunnel.used
		}
	}

	for _, session := range c.sessions {
		if session.used.After(used) {
			used = session.used
		}
	}

	return used
}

func (c *Connection) Services() map[string][]uint16 {
	c.lock.Lock()
	defer c.lock.Unlock()

	services := make(map[string][]uint16)
	for host, ports := range c.services {
		for port, ok := range ports {
			if ok {
				services[host] = append(services[host], port)
			}
		}
	}

	return services
}

func (c *Connection) reportStatus(status json.RawMessage) {
	c.lock.Lock()
	c.lock.Unlock()

	c.status = status
}

func (c *Connection) writeStatus() error {
	if c.gateway.statusDirectory == "" {
		return nil
	}

	encoded, err := json.MarshalIndent(c.gatherStatus(true), "", "  ")
	if err != nil {
		return err
	}

	filename := filepath.Join(c.gateway.statusDirectory, c.user+".json")
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if _, err := file.WriteString("\n"); err != nil {
		return err
	}

	return nil
}

func (c *Connection) gatherStatus(includeReportedStatus bool) map[string]interface{} {
	c.lock.Lock()
	defer c.lock.Unlock()

	// services
	services := make(map[string][]uint16)
	for host, ports := range c.services {
		for port, ok := range ports {
			if ok {
				services[host] = append(services[host], port)
			}
		}
	}

	used := c.used
	bytesRead := c.bytesRead
	bytesWritten := c.bytesWritten

	tunnels := make([]interface{}, 0, len(c.tunnels))
	for _, tunnel := range c.tunnels {
		tunnels = append(tunnels, tunnel.gatherStatus())
		bytesRead += tunnel.bytesRead
		bytesWritten += tunnel.bytesWritten
		if tunnel.used.After(used) {
			used = tunnel.used
		}
	}

	sessions := make([]interface{}, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session.gatherStatus())
		bytesRead += session.bytesRead
		bytesWritten += session.bytesWritten
		if session.used.After(used) {
			used = session.used
		}
	}

	status := map[string]interface{}{
		"user":            c.user,
		"admin":           c.admin,
		"address":         c.remoteAddr.String(),
		"location":        c.location,
		"sessions":        sessions,
		"sessions_closed": c.sessionsClosed,
		"tunnels":         tunnels,
		"tunnels_closed":  c.tunnelsClosed,
		"created":         c.created.Unix(),
		"used":            c.used.Unix(),
		"up_time":         uint64(time.Since(c.created).Seconds()),
		"idle_time":       uint64(time.Since(c.used).Seconds()),
		"bytes_read":      c.bytesRead,
		"bytes_written":   c.bytesWritten,
		"services":        services,
	}

	if includeReportedStatus {
		status["status"] = c.status
	}

	return status
}

func (c *Connection) lookupService(host string, port uint16) bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.services[host]; !ok {
		return false
	}

	return c.services[host][port]
}

func (c *Connection) registerService(host string, port uint16) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.services[host]; !ok {
		c.services[host] = make(map[uint16]bool)
	}
	if c.services[host][port] {
		return ErrServiceAlreadyRegistered
	}
	c.services[host][port] = true

	log.Infof("registered service: user = %s, host = %s, port = %d", c.user, host, port)
	return nil
}

func (c *Connection) deregisterService(host string, port uint16) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.services[host]; ok {
		delete(c.services[host], port)
	}

	log.Infof("deregistered service: user = %s, host = %s, port = %d", c.user, host, port)
	return nil
}

func (c *Connection) Handle(requests <-chan *ssh.Request, channels <-chan ssh.NewChannel) {
	go c.handleRequests(requests)
	go c.handleChannels(channels)
}

func (c *Connection) handleRequests(requests <-chan *ssh.Request) {
	defer c.Close()

	for request := range requests {
		c.used = time.Now()
		go c.handleRequest(request)
	}
}

func (c *Connection) handleChannels(channels <-chan ssh.NewChannel) {
	defer c.Close()

	for channel := range channels {
		c.used = time.Now()
		go c.handleChannel(channel)
	}
}

func (c *Connection) handleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	ok := false
	switch request.Type {
	case "tcpip-forward":
		request, err := unmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Errorf("failed to decode request: %s", err)
			break
		}

		if request.Port == 0 {
			log.Errorf("requested forwarding port is not allowed: %d", request.Port)
			break
		}

		if err := c.registerService(request.Host, uint16(request.Port)); err != nil {
			log.Errorf("failed to register service in connection: %s", err)
			break
		}

		ok = true

	case "cancel-tcpip-forward":
		request, err := unmarshalForwardRequest(request.Payload)
		if err != nil {
			log.Errorf("failed to decode request: %s", err)
			break
		}

		if err := c.deregisterService(request.Host, uint16(request.Port)); err != nil {
			log.Errorf("failed to register service in connection: %s", err)
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

func (c *Connection) handleChannel(newChannel ssh.NewChannel) {
	log.Debugf("new channel: type = %s, data = %v", newChannel.ChannelType(), newChannel.ExtraData())

	ok := false
	rejection := ssh.UnknownChannelType

	switch newChannel.ChannelType() {
	case "session":
		ok, rejection = c.handleSessionChannel(newChannel)
	case "direct-tcpip":
		ok, rejection = c.handleTunnelChannel(newChannel)
	}

	if !ok {
		// reject the channel
		if err := newChannel.Reject(rejection, ""); err != nil {
			log.Warningf("failed to reject channel: %s", err)
		}
	}
}

func (c *Connection) handleSessionChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason) {
	if len(newChannel.ExtraData()) > 0 {
		// do not accept extra data in connection channel request
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

	session, err := newSession(c, channel, newChannel.ChannelType(), newChannel.ExtraData())
	if err != nil {
		log.Errorf("failed to create accepted channel: %s", err)
		return true, 0
	}
	c.addSession(session)

	// no failure
	session.Handle(requests)

	// do not close channel on exit
	channel = nil
	return true, 0
}

func (c *Connection) handleTunnelChannel(newChannel ssh.NewChannel) (bool, ssh.RejectionReason) {

	data, err := unmarshalTunnelData(newChannel.ExtraData())
	if err != nil {
		return false, ssh.UnknownChannelType
	}

	// look up connection by name
	connection, host, port := c.gateway.lookupConnectionService(data.Host, uint16(data.Port))
	if connection == nil {
		return false, ssh.ConnectionFailed
	}

	// see if this connection is allowed
	if !c.admin {
		log.Warningf("no permission to port forward: user = %s", c.user)
		return false, ssh.Prohibited
	}

	// found the service, attempt to open a channel
	data2 := &tunnelData{
		Host:          host,
		Port:          uint32(port),
		OriginAddress: data.OriginAddress,
		OriginPort:    data.OriginPort,
	}

	tunnel2, err := connection.openTunnel("forwarded-tcpip", marshalTunnelData(data2), map[string]interface{}{
		"origin": data.OriginAddress,
		"from": map[string]interface{}{
			"address": c.remoteAddr.String(),
			"user":    c.user,
		},
		"service": map[string]interface{}{
			"host": data.Host,
			"port": data.Port,
		},
	})
	if err != nil {
		return false, ssh.ConnectionFailed
	}
	defer func() {
		if tunnel2 != nil {
			tunnel2.Close()
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

	tunnel, err := newTunnel(c, channel, newChannel.ChannelType(), newChannel.ExtraData(), map[string]interface{}{
		"origin": data.OriginAddress,
		"to": map[string]interface{}{
			"user":    connection.user,
			"address": connection.remoteAddr.String(),
		},
		"service": map[string]interface{}{
			"host": data.Host,
			"port": data.Port,
		},
	})
	if err != nil {
		log.Errorf("failed to create accepted channel: %s", err)
		return true, 0
	}
	c.addTunnel(tunnel)

	// no failure
	tunnel.Handle(requests, tunnel2)

	// do not close channel on exit
	channel = nil
	tunnel2 = nil
	return true, 0
}

// open a channel from the server to the client side
func (c *Connection) openTunnel(channelType string, extraData []byte, metadata map[string]interface{}) (*Tunnel, error) {
	log.Debugf("opening channel: type = %s, data = %v", channelType, extraData)

	channel, requests, err := c.conn.OpenChannel(channelType, extraData)
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

	tunnel, err := newTunnel(c, channel, channelType, extraData, metadata)
	if err != nil {
		return nil, err
	}
	c.addTunnel(tunnel)

	// no failure
	tunnel.Handle(requests, nil)

	// do not close channel on exit
	channel = nil
	return tunnel, nil
}
