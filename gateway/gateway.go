package gateway

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/op/go-logging"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
)

var log = logging.MustGetLogger("gateway")

// an instance of gateway, contains runtime states
type Gateway interface {
	Close()

	HandleConnection(net.Conn)
	ScavengeConnections(time.Duration)

	ListUsers() (interface{}, error)
	GetUser(string) (interface{}, error)
}

type gateway struct {
	database         db.Database
	sshConfig        *ssh.ServerConfig
	connectionsIndex map[string][]*connection
	connectionsList  []*connection
	lock             *sync.Mutex
	closeOnce        sync.Once
}

// creates a new instance of gateway
func Open(database db.Database, sshConfig *ssh.ServerConfig) (Gateway, error) {
	return &gateway{
		database:         database,
		sshConfig:        sshConfig,
		connectionsIndex: make(map[string][]*connection),
		connectionsList:  make([]*connection, 0),
		lock:             &sync.Mutex{},
	}, nil
}

// close the gateway instance
func (self *gateway) Close() {
	self.closeOnce.Do(func() {
		for _, connection := range self.listConnections() {
			connection.close()
		}
	})
}

// handle an incoming ssh connection
func (self *gateway) HandleConnection(c net.Conn) {
	log.Infof("new tcp connection: remote = %s, local = %s", c.RemoteAddr(), c.LocalAddr())
	defer func() {
		if c != nil {
			if err := c.Close(); err != nil {
				log.Warningf("failed to close connection: %s", err)
			}
		}
	}()

	usage := newUsage()
	conn, channels, requests, err := ssh.NewServerConn(wrapConn(c, usage), self.sshConfig)
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

	// create a connection and handle it
	connection := newConnection(self, conn, usage)
	self.addConnection(connection)

	// handle requests and channels on this connection
	go connection.handleRequests(requests)
	go connection.handleChannels(channels)

	// don't close connection on success
	conn = nil
	c = nil
}

// add connection to the list of connections
func (self *gateway) addConnection(c *connection) {
	self.lock.Lock()
	defer self.lock.Unlock()

	self.connectionsIndex[c.user] = append([]*connection{c}, self.connectionsIndex[c.user]...)
	self.connectionsList = append([]*connection{c}, self.connectionsList...)
}

func (self *gateway) deleteConnection(c *connection) {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]*connection, 0, len(self.connectionsIndex[c.user]))
	for _, connection := range self.connectionsIndex[c.user] {
		if connection != c {
			connections = append(connections, connection)
		}
	}
	self.connectionsIndex[c.user] = connections

	connections = make([]*connection, 0, len(self.connectionsList))
	for _, connection := range self.connectionsList {
		if connection != c {
			connections = append(connections, connection)
		}
	}
	self.connectionsList = connections
}

func (self *gateway) lookupConnectionService(host string, port uint16) (*connection, string, uint16) {
	self.lock.Lock()
	defer self.lock.Unlock()

	parts := strings.Split(host, ".")
	for i := range parts {
		host := strings.Join(parts[:i], ".")
		user := strings.Join(parts[i:], ".")

		for _, connection := range self.connectionsIndex[user] {
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
func (self *gateway) listConnections() []*connection {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]*connection, len(self.connectionsList))
	copy(connections, self.connectionsList)
	return connections
}

// scavenge timed out connections
func (self *gateway) ScavengeConnections(timeout time.Duration) {
	for _, connection := range self.listConnections() {
		idle := time.Since(connection.getUsed())
		if idle > timeout {
			log.Infof("scavenge: connection for %s timed out after %d seconds", connection.user, uint64(idle.Seconds()))
			connection.close()
		}
	}
}

func (self *gateway) gatherStatus() map[string]interface{} {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]interface{}, 0, len(self.connectionsList))
	for _, connection := range self.connectionsList {
		connections = append(connections, connection.gatherStatus())
	}

	return map[string]interface{}{
		"connections": connections,
	}
}

func (self *gateway) ListUsers() (interface{}, error) {
	users, err := self.database.ListUsers()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"users": users,
		"meta": map[string]interface{}{
			"total_count": len(users),
		},
	}, nil
}

func (self *gateway) GetUser(id string) (interface{}, error) {
	user, err := self.database.GetUser(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}

	var connections []*connectionStatus
	func() {
		self.lock.Lock()
		defer self.lock.Unlock()

		for _, connection := range self.connectionsList {
			if connection.user != id {
				continue
			}
			connections = append(connections, connection.gatherStatus())
		}
	}()

	return map[string]interface{}{
		"user":        user,
		"connections": connections,
	}, nil
}
