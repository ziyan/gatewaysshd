package gateway

import (
	"bytes"
	"net"
	"sync"

	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

type Gateway struct {
	config   *ssh.ServerConfig
	sessions map[string][]*Session
	lock     *sync.Mutex
}

func NewGateway(caPublicKey, hostPrivateKey []byte) (*Gateway, error) {

	// parse certificate authority
	ca, _, _, _, err := ssh.ParseAuthorizedKey(caPublicKey)
	if err != nil {
		return nil, err
	}

	// parse host key
	host, err := ssh.ParsePrivateKey(hostPrivateKey)
	if err != nil {
		return nil, err
	}

	// create checker
	// TODO: implement IsRevoked
	checker := &ssh.CertChecker{
		IsAuthority: func(key ssh.PublicKey) bool {
			// TODO: logging
			return bytes.Compare(ca.Marshal(), key.Marshal()) == 0
		},
	}

	// create server config
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			// TODO: logging
			return checker.Authenticate(meta, key)
		},
	}
	config.AddHostKey(host)

	return &Gateway{
		config:   config,
		sessions: make(map[string][]*Session),
		lock:     &sync.Mutex{},
	}, nil
}

func (g *Gateway) HandleConnection(c net.Conn) {
	glog.Infof("gatewaysshd: new tcp connection from %s to %s", c.RemoteAddr(), c.LocalAddr())

	connection, channels, requests, err := ssh.NewServerConn(c, g.config)
	if err != nil {
		glog.Infof("gatewaysshd: failed during ssh handshake: %s", err)
		return
	}

	// create a session and handle it
	session, err := NewSession(g, connection)
	if err != nil {
		glog.Errorf("gatewaysshd: failed to create session: %s", err)
		if err := connection.Close(); err != nil {
			glog.Warningf("gatewaysshd: failed to close connection: %s", err)
		}
		return
	}
	g.AddSession(session)

	go session.HandleRequests(requests)
	go session.HandleChannels(channels)
}

// add session to the list of sessions
func (g *Gateway) AddSession(s *Session) {
	g.lock.Lock()
	defer g.lock.Unlock()
	g.sessions[s.User()] = append([]*Session{s}, g.sessions[s.User()]...)

}

func (g *Gateway) DeleteSession(s *Session) {
	g.lock.Lock()
	defer g.lock.Unlock()

	// filter the list of sessions
	sessions := make([]*Session, 0, len(g.sessions[s.User()]))
	for _, session := range g.sessions[s.User()] {
		if session != s {
			sessions = append(sessions, s)
		}
	}
	g.sessions[s.User()] = sessions
}

func (g *Gateway) LookupSessionForService(user, service string, port uint16) *Session {
	g.lock.Lock()
	defer g.lock.Unlock()

	for _, session := range g.sessions[user] {
		if session.LookupService(service, port) {
			return session
		}
	}

	return nil
}
