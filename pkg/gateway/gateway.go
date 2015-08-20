package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

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
	g.sessions[s.User()] = append([]*Session{s}, g.sessions[s.User()]...)

}

func (g *Gateway) DeleteSession(s *Session) {
	g.lock.Lock()
	defer g.lock.Unlock()

	// filter the list of sessions
	sessions := make([]*Session, 0, len(g.sessions[s.User()]))
	for _, session := range g.sessions[s.User()] {
		if session != s {
			sessions = append(sessions, session)
		}
	}
	g.sessions[s.User()] = sessions
}

func (g *Gateway) LookupSessionService(host string, port uint16) (*Session, string, uint16) {
	g.lock.Lock()
	defer g.lock.Unlock()

	parts := strings.Split(host, ".")
	for i := range parts {
		host := strings.Join(parts[:i], ".")
		user := strings.Join(parts[i:], ".")

		for _, session := range g.sessions[user] {
			if session.LookupService(host, port) {
				glog.V(1).Infof("lookup: found service: user = %s, host = %s, port = %d", user, host, port)
				return session, host, port
			}
		}
	}

	glog.V(1).Infof("lookup: did not find service: host = %s, port = %d", host, port)
	return nil, "", 0
}

func (g *Gateway) ListSessions(w io.Writer) {
	g.lock.Lock()
	defer g.lock.Unlock()

	sessions := make(SessionsList, 0)
	for _, s := range g.sessions {
		sessions = append(sessions, s...)
	}
	sort.Sort(sessions)

	for _, session := range sessions {

		services := make([]string, 0)
		for host, ports := range session.Services() {
			for _, port := range ports {
				services = append(services, fmt.Sprintf("%s:%d", host, port))
			}
		}

		fmt.Fprintf(w, "%s\t%v\t%d\t%s\n", session.User(), session.RemoteAddr(), uint64(time.Since(session.Created()).Seconds()), strings.Join(services, ","))
	}
}
