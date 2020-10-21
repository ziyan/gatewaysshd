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

type Settings struct {
	Version string
}

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
	settings         *Settings
	connectionsIndex map[string][]*connection
	connectionsList  []*connection
	lock             sync.Mutex
}

// creates a new instance of gateway
func Open(database db.Database, sshConfig *ssh.ServerConfig, settings *Settings) (Gateway, error) {
	return &gateway{
		database:         database,
		sshConfig:        sshConfig,
		settings:         settings,
		connectionsIndex: make(map[string][]*connection),
		connectionsList:  make([]*connection, 0),
	}, nil
}

// close the gateway instance
func (self *gateway) Close() {
	for _, connection := range self.listConnections() {
		connection.close()
	}
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
	handleConnection(self, conn, channels, requests, usage)

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
				log.Debugf("lookup: found service: connection = %s, host = %s, port = %d", connection, host, port)
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
			log.Infof("scavenge: connection %s timed out after %s", connection, idle)
			connection.close()
		}
	}
}

func (self *gateway) kickUser(user string) error {
	for _, connection := range self.listConnections() {
		if connection.user == user {
			log.Infof("kick: closing connection %s", connection)
			connection.close()
		}
	}
	return nil
}

func (self *gateway) gatherStatus(user string) map[string]interface{} {
	self.lock.Lock()
	defer self.lock.Unlock()

	connections := make([]interface{}, 0, len(self.connectionsList))
	for _, connection := range self.connectionsList {
		if user == "" || connection.user == user {
			connections = append(connections, connection.gatherStatus())
		}
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
	func() {
		self.lock.Lock()
		defer self.lock.Unlock()
		for _, user := range users {
			user.Status = nil
			user.Online = false
			for _, connection := range self.connectionsList {
				if connection.user == user.ID {
					user.Online = true
					break
				}
			}
		}
	}()
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
			if connection.user == user.ID {
				connections = append(connections, connection.gatherStatus())
			}
		}
	}()

	user.Online = len(connections) > 0
	user.Connections = connections

	return map[string]interface{}{
		"user": user,
	}, nil
}
