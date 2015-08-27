package gateway

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

var (
	ErrInvalidCertificate = errors.New("gatewaysshd: invalid certificate")
)

type Gateway struct {
	config        *ssh.ServerConfig
	sessionsIndex map[string][]*Session
	sessionsList  []*Session
	lock          *sync.Mutex
}

func NewGateway(serverVersion string, caPublicKey, hostCertificate, hostPrivateKey []byte) (*Gateway, error) {

	// parse certificate authority
	ca, _, _, _, err := ssh.ParseAuthorizedKey(caPublicKey)
	if err != nil {
		return nil, err
	}
	glog.V(9).Infof("auth: ca_public_key = %v", ca)

	// parse host certificate
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(hostCertificate)
	if err != nil {
		return nil, err
	}
	cert, ok := parsed.(*ssh.Certificate)
	if !ok {
		return nil, ErrInvalidCertificate
	}

	principal := "localhost"
	if len(cert.ValidPrincipals) > 0 {
		principal = cert.ValidPrincipals[0]
	}

	// parse host key
	key, err := ssh.ParsePrivateKey(hostPrivateKey)
	if err != nil {
		return nil, err
	}

	// create signer for host
	host, err := ssh.NewCertSigner(cert, key)
	if err != nil {
		return nil, err
	}
	glog.V(9).Infof("auth: host_public_key = %v", key.PublicKey())

	// create checker
	// TODO: implement IsRevoked
	checker := &ssh.CertChecker{
		IsAuthority: func(key ssh.PublicKey) bool {
			if bytes.Compare(ca.Marshal(), key.Marshal()) == 0 {
				return true
			}
			glog.V(9).Infof("auth: unknown authority: %v", key)
			return false
		},
		IsRevoked: func(cert *ssh.Certificate) bool {
			return false
		},
	}

	// test the checker
	glog.V(9).Infof("auth: testing host certificate using principal: %s", principal)
	if err := checker.CheckCert(principal, cert); err != nil {
		return nil, err
	}

	// create server config
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			glog.V(9).Infof("auth: remote = %s, local = %s, public_key = %v", meta.RemoteAddr(), meta.LocalAddr(), key)
			return checker.Authenticate(meta, key)
		},
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			glog.V(2).Infof("auth: remote = %s, local = %s, method = %s, error = %v", meta.RemoteAddr(), meta.LocalAddr(), method, err)
		},
		ServerVersion: serverVersion,
	}
	config.AddHostKey(host)

	return &Gateway{
		config:        config,
		sessionsIndex: make(map[string][]*Session),
		sessionsList:  make([]*Session, 0),
		lock:          &sync.Mutex{},
	}, nil
}

func (g *Gateway) HandleConnection(c net.Conn) {
	glog.V(1).Infof("new tcp connection: remote = %s, local = %s", c.RemoteAddr(), c.LocalAddr())

	connection, channels, requests, err := ssh.NewServerConn(c, g.config)
	if err != nil {
		glog.Warningf("failed during ssh handshake: %s", err)
		return
	}

	// create a session and handle it
	session, err := NewSession(g, connection)
	if err != nil {
		glog.Errorf("failed to create session: %s", err)
		if err := connection.Close(); err != nil {
			glog.Warningf("failed to close connection: %s", err)
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

	g.sessionsIndex[s.User()] = append([]*Session{s}, g.sessionsIndex[s.User()]...)
	g.sessionsList = append([]*Session{s}, g.sessionsList...)

}

func (g *Gateway) DeleteSession(s *Session) {
	g.lock.Lock()
	defer g.lock.Unlock()

	sessions := make([]*Session, 0, len(g.sessionsIndex[s.User()]))
	for _, session := range g.sessionsIndex[s.User()] {
		if session != s {
			sessions = append(sessions, session)
		}
	}
	g.sessionsIndex[s.User()] = sessions

	sessions = make([]*Session, 0, len(g.sessionsList))
	for _, session := range g.sessionsList {
		if session != s {
			sessions = append(sessions, session)
		}
	}
	g.sessionsList = sessions
}

func (g *Gateway) LookupSessionService(host string, port uint16) (*Session, string, uint16) {
	g.lock.Lock()
	defer g.lock.Unlock()

	parts := strings.Split(host, ".")
	for i := range parts {
		host := strings.Join(parts[:i], ".")
		user := strings.Join(parts[i:], ".")

		for _, session := range g.sessionsIndex[user] {
			if session.LookupService(host, port) {
				glog.V(1).Infof("lookup: found service: user = %s, host = %s, port = %d", user, host, port)
				return session, host, port
			}
		}
	}

	glog.V(1).Infof("lookup: failed to find service: host = %s, port = %d", host, port)
	return nil, "", 0
}

func (g *Gateway) ListSessions() []interface{} {
	g.lock.Lock()
	defer g.lock.Unlock()

	list := make([]interface{}, 0, len(g.sessionsList))
	for _, session := range g.sessionsList {
		list = append(list, map[string]interface{}{
			"user":           session.User(),
			"address":        session.RemoteAddr().String(),
			"channels_count": session.ChannelsCount(),
			"timestamp":      session.Timestamp().Unix(),
			"uptime":         uint64(time.Since(session.Timestamp()).Seconds()),
			"services":       session.Services(),
		})
	}

	return list
}
