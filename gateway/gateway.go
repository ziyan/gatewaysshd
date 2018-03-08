package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	statusFile       string
	statusDirectory  string
	config           *ssh.ServerConfig
	connectionsIndex map[string][]*Connection
	connectionsList  []*Connection
	lock             *sync.Mutex
	closeOnce        sync.Once
}

func NewGateway(serverVersion string, caPublicKey, hostCertificate, hostPrivateKey []byte, revocationList, statusFile, statusDirectory string) (*Gateway, error) {

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
		statusFile:       statusFile,
		statusDirectory:  statusDirectory,
		config:           config,
		connectionsIndex: make(map[string][]*Connection),
		connectionsList:  make([]*Connection, 0),
		lock:             &sync.Mutex{},
	}, nil
}

func (g *Gateway) Close() {
	g.closeOnce.Do(func() {
		for _, connection := range g.Connections() {
			connection.Close()
		}
	})
}

func (g *Gateway) HandleConnection(c net.Conn) {
	log.Infof("new tcp connection: remote = %s, local = %s", c.RemoteAddr(), c.LocalAddr())

	conn, channels, requests, err := ssh.NewServerConn(c, g.config)
	if err != nil {
		log.Warningf("failed during ssh handshake: %s", err)
		return
	}

	// create a connection and handle it
	connection, err := newConnection(g, conn)
	if err != nil {
		log.Errorf("failed to create connection: %s", err)
		if err := conn.Close(); err != nil {
			log.Warningf("failed to close connection: %s", err)
		}
		return
	}
	g.addConnection(connection)

	connection.Handle(requests, channels)
}

// add connection to the list of connections
func (g *Gateway) addConnection(c *Connection) {
	g.lock.Lock()
	defer g.lock.Unlock()

	g.connectionsIndex[c.user] = append([]*Connection{c}, g.connectionsIndex[c.user]...)
	g.connectionsList = append([]*Connection{c}, g.connectionsList...)
}

func (g *Gateway) deleteConnection(c *Connection) {
	g.lock.Lock()
	defer g.lock.Unlock()

	connections := make([]*Connection, 0, len(g.connectionsIndex[c.user]))
	for _, connection := range g.connectionsIndex[c.user] {
		if connection != c {
			connections = append(connections, connection)
		}
	}
	g.connectionsIndex[c.user] = connections

	connections = make([]*Connection, 0, len(g.connectionsList))
	for _, connection := range g.connectionsList {
		if connection != c {
			connections = append(connections, connection)
		}
	}
	g.connectionsList = connections
}

func (g *Gateway) lookupConnectionService(host string, port uint16) (*Connection, string, uint16) {
	g.lock.Lock()
	defer g.lock.Unlock()

	parts := strings.Split(host, ".")
	for i := range parts {
		host := strings.Join(parts[:i], ".")
		user := strings.Join(parts[i:], ".")

		for _, connection := range g.connectionsIndex[user] {
			if connection.lookupService(host, port) {
				log.Infof("lookup: found service: user = %s, host = %s, port = %d", user, host, port)
				return connection, host, port
			}
		}
	}

	log.Infof("lookup: failed to find service: host = %s, port = %d", host, port)
	return nil, "", 0
}

func (g *Gateway) Connections() []*Connection {
	g.lock.Lock()
	defer g.lock.Unlock()

	connections := make([]*Connection, len(g.connectionsList))
	copy(connections, g.connectionsList)
	return connections
}

func (g *Gateway) ScavengeConnections(timeout time.Duration) {
	for _, connection := range g.Connections() {
		idle := time.Since(connection.Used())
		if idle > timeout {
			log.Infof("scavenge: connection for %s timed out after %d seconds", connection.user, uint64(idle.Seconds()))
			connection.Close()
		}
	}
}

func (g *Gateway) gatherStatus() map[string]interface{} {
	g.lock.Lock()
	defer g.lock.Unlock()

	connections := make([]interface{}, 0, len(g.connectionsList))
	for _, connection := range g.connectionsList {
		connections = append(connections, connection.gatherStatus())
	}

	return map[string]interface{}{
		"connections": connections,
	}
}

func (g *Gateway) WriteStatus() {
	if err := g.writeStatus(); err != nil {
		log.Warningf("failed to write status: %s: %s", g.statusFile, err)
	}
}

func (g *Gateway) writeStatus() error {
	if g.statusFile == "" {
		return nil
	}

	encoded, err := json.MarshalIndent(g.gatherStatus(), "", "  ")
	if err != nil {
		return err
	}

	file, err := os.OpenFile(g.statusFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
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
