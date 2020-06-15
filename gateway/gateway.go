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

// an instance of gateway, contains runtime states
type Gateway struct {
	geoipDatabase    string
	database         *Database
	config           *ssh.ServerConfig
	connectionsIndex map[string][]*Connection
	connectionsList  []*Connection
	lock             *sync.Mutex
	closeOnce        sync.Once
}

// creates a new instance of gateway
func NewGateway(serverVersion string, caPublicKeys, hostCertificate, hostPrivateKey []byte, revocationList string, geoipDatabase string, database *Database) (*Gateway, error) {

	// parse certificate authority
	var cas []ssh.PublicKey
	for len(caPublicKeys) > 0 {
		ca, _, _, rest, err := ssh.ParseAuthorizedKey(caPublicKeys)
		if err != nil {
			return nil, err
		}
		log.Debugf("auth: ca_public_key = %v", ca)
		cas = append(cas, ca)
		caPublicKeys = rest
	}

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
			for _, ca := range cas {
				if bytes.Compare(ca.Marshal(), key.Marshal()) == 0 {
					return true
				}
			}
			log.Warningf("auth: unknown authority: %v", key)
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
						log.Warningf("auth: certificate revoked by revocation list: %s/%d", cert.KeyId, cert.Serial)
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
			if err != nil {
				return nil, err
			}
            return permissions, nil
		},
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			log.Debugf("auth: remote = %s, local = %s, method = %s, error = %v", meta.RemoteAddr(), meta.LocalAddr(), method, err)
		},
		ServerVersion: serverVersion,
	}
	config.AddHostKey(host)

	return &Gateway{
		geoipDatabase:    geoipDatabase,
		database:         database,
		config:           config,
		connectionsIndex: make(map[string][]*Connection),
		connectionsList:  make([]*Connection, 0),
		lock:             &sync.Mutex{},
	}, nil
}

// close the gateway instance
func (g *Gateway) Close() {
	g.closeOnce.Do(func() {
		for _, connection := range g.Connections() {
			connection.Close()
		}
	})
}

// handle an incoming ssh connection
func (g *Gateway) HandleConnection(c net.Conn) {
	log.Infof("new tcp connection: remote = %s, local = %s", c.RemoteAddr(), c.LocalAddr())
	defer func() {
		if c != nil {
			if err := c.Close(); err != nil {
				log.Warningf("failed to close connection: %s", err)
			}
		}
	}()

	usage := newUsage()
	conn, channels, requests, err := ssh.NewServerConn(wrapConn(c, usage), g.config)
	if err != nil {
		log.Warningf("failed during ssh handshake: %s", err)
		return
	}
	defer func() {
		if conn != nil {
			if err := conn.Close(); err != nil {
				log.Warningf("failed to close connection: %s", err)
			}
		}
	}()

	// look up connection
	location := lookupLocation(g.geoipDatabase, c.RemoteAddr().(*net.TCPAddr).IP)

	// create a connection and handle it
	connection := newConnection(g, conn, usage, location)
	g.addConnection(connection)

	// handle requests and channels on this connection
	go connection.handleRequests(requests)
	go connection.handleChannels(channels)

	// don't close connection on success
	conn = nil
	c = nil
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
				log.Debugf("lookup: found service: user = %s, host = %s, port = %d", user, host, port)
				return connection, host, port
			}
		}
	}

	log.Debugf("lookup: failed to find service: host = %s, port = %d", host, port)
	return nil, "", 0
}

// returns a list of connections
func (g *Gateway) Connections() []*Connection {
	g.lock.Lock()
	defer g.lock.Unlock()

	connections := make([]*Connection, len(g.connectionsList))
	copy(connections, g.connectionsList)
	return connections
}

// scavenge timed out connections
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

func (g *Gateway) ListUsers() (interface{}, error) {
	users := make(map[string]map[string]interface{})
	connectionsCount := make(map[string]int)

	// first collect users from live connections
	func() {
		g.lock.Lock()
		defer g.lock.Unlock()

		for _, connection := range g.connectionsList {
			if _, ok := users[connection.user]; !ok {
				users[connection.user] = map[string]interface{}{
					"id":       connection.user,
					"address":  connection.remoteAddr.String(),
					"location": connection.location,
					"used":     connection.usage.used.Unix(),
				}
			}
			connectionsCount[connection.user]++
		}
	}()

	// then collect more users from database
	models, err := g.database.listUsers()
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		if _, ok := users[model.ID]; !ok {
			users[model.ID] = map[string]interface{}{
				"id":       model.ID,
				"address":  model.Address,
				"location": model.Location,
				"used":     model.Used,
			}
		}
		users[model.ID]["database"] = true
	}

	// make it into a list
	usersList := make([]interface{}, 0, len(users))
	for id, user := range users {
		user["connections_count"] = connectionsCount[id]
		usersList = append(usersList, user)
	}
	return map[string]interface{}{
		"users": usersList,
		"meta": map[string]interface{}{
			"total_count": len(usersList),
		},
	}, nil
}

func (g *Gateway) GetUser(id string) (interface{}, error) {
	connections := make([]interface{}, 0)
	user := map[string]interface{}{
		"id": id,
	}

	func() {
		g.lock.Lock()
		defer g.lock.Unlock()

		for _, connection := range g.connectionsList {
			if connection.user != id {
				continue
			}
			if len(connections) == 0 {
				user["address"] = connection.remoteAddr.String()
				user["location"] = connection.location
				user["used"] = connection.usage.used.Unix()
			}
			connectionStatus := connection.gatherStatus()
			if connectionStatus["status"] != nil {
				if _, ok := user["status"]; !ok {
					user["status"] = connectionStatus["status"]
				}
			}
			delete(connectionStatus, "status")
			connections = append(connections, connectionStatus)
		}
	}()

	model, err := g.database.getUser(id)
	if err != nil {
		return nil, err
	}
	if model == nil && len(connections) == 0 {
		return nil, nil
	}

	if model != nil {
		if len(connections) == 0 {
			user["address"] = model.Address
			user["location"] = model.Location
			user["used"] = model.Used
		}
		user["database"] = true
		if model.Status != nil {
			if _, ok := user["status"]; !ok {
				user["status"] = model.Status
			}
		}
	}

	user["connections"] = connections
	return map[string]interface{}{
		"user": user,
	}, nil
}
