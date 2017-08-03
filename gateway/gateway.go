package gateway

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/op/go-logging"
	"golang.org/x/crypto/ssh"
)

var log = logging.MustGetLogger("gateway")

var (
	ErrInvalidCertificate = errors.New("gatewaysshd: invalid certificate")
)

type Gateway struct {
	config        *ssh.ServerConfig
	sessionsIndex map[string][]*Session
	sessionsList  []*Session
	lock          *sync.Mutex
	closeOnce     sync.Once
}

func NewGateway(serverVersion string, caPublicKey, hostCertificate, hostPrivateKey []byte, revocationList string) (*Gateway, error) {

	// parse certificate authority
	ca, _, _, _, err := ssh.ParseAuthorizedKey(caPublicKey)
	if err != nil {
		return nil, err
	}
	log.Debugf("auth: ca_public_key = %v", ca)

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
	log.Debugf("auth: host_public_key = %v", key.PublicKey())

	// create checker
	checker := &ssh.CertChecker{
		IsUserAuthority: func(key ssh.PublicKey) bool {
			if bytes.Compare(ca.Marshal(), key.Marshal()) == 0 {
				return true
			}
			log.Errorf("auth: unknown authority: %v", key)
			return false
		},
		IsRevoked: func(cert *ssh.Certificate) bool {
			// if revocation list file does not exist, assume everything is good
			if _, err := os.Stat(revocationList); os.IsNotExist(err) {
				return false
			}

			file, err := os.Open(revocationList)
			if err != nil {
				log.Errorf("auth: failed to open revocation list: %s", err)
				return true
			}
			defer file.Close()

			// if line matches any of the following, it is considered revoked
			matches := []string{
				fmt.Sprintf("%s\n", cert.KeyId),
				fmt.Sprintf("%d\n", cert.Serial),
				fmt.Sprintf("%s/%d\n", cert.KeyId, cert.Serial),
			}

			reader := bufio.NewReader(file)
			for {
				line, err := reader.ReadString('\n')
				if err == io.EOF {
					break
				}
				if err != nil {
					log.Errorf("auth: failed to read revocation list: %s", err)
					return true
				}
				if line[0] == '#' || line == "\n" {
					continue
				}
				for _, match := range matches {
					if line == match {
						log.Errorf("auth: certificate revoked by revocation list: %s/%d", cert.KeyId, cert.Serial)
						return true
					}
				}
			}
			return false
		},
	}

	// test the checker
	log.Debugf("auth: testing host certificate using principal: %s", principal)
	if err := checker.CheckCert(principal, cert); err != nil {
		return nil, err
	}

	// create server config
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			permissions, err := checker.Authenticate(meta, key)
			log.Debugf("auth: remote = %s, local = %s, public_key = %v, permissions = %v, err = %v", meta.RemoteAddr(), meta.LocalAddr(), key, permissions, err)
			return permissions, err
		},
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			log.Debugf("auth: remote = %s, local = %s, method = %s, error = %v", meta.RemoteAddr(), meta.LocalAddr(), method, err)
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

func (g *Gateway) Close() {
	g.closeOnce.Do(func() {
		for _, session := range g.Sessions() {
			session.Close()
		}
	})
}

func (g *Gateway) HandleConnection(c net.Conn) {
	log.Infof("new tcp connection: remote = %s, local = %s", c.RemoteAddr(), c.LocalAddr())

	connection, channels, requests, err := ssh.NewServerConn(c, g.config)
	if err != nil {
		log.Warningf("failed during ssh handshake: %s", err)
		return
	}

	// create a session and handle it
	session, err := NewSession(g, connection)
	if err != nil {
		log.Errorf("failed to create session: %s", err)
		if err := connection.Close(); err != nil {
			log.Warningf("failed to close connection: %s", err)
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
				log.Infof("lookup: found service: user = %s, host = %s, port = %d", user, host, port)
				return session, host, port
			}
		}
	}

	log.Infof("lookup: failed to find service: host = %s, port = %d", host, port)
	return nil, "", 0
}

func (g *Gateway) Sessions() []*Session {
	g.lock.Lock()
	defer g.lock.Unlock()

	sessions := make([]*Session, len(g.sessionsList))
	copy(sessions, g.sessionsList)
	return sessions
}

func (g *Gateway) ScavengeSessions(timeout time.Duration) {
	for _, session := range g.Sessions() {
		idle := time.Since(session.Used())
		if idle > timeout {
			log.Infof("scavenge: session for %s timed out after %d seconds", session.User(), uint64(idle.Seconds()))
			session.Close()
		}
	}
}

func (g *Gateway) Status() map[string]interface{} {
	g.lock.Lock()
	defer g.lock.Unlock()

	sessions := make([]interface{}, 0, len(g.sessionsList))
	for _, session := range g.sessionsList {
		sessions = append(sessions, session.Status())
	}

	return map[string]interface{}{
		"sessions": sessions,
	}
}
