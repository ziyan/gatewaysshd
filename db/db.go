// Database
//
// user - store user special attributes, such as non-ldap user, groups
// node - store gateway nodes participating in the mesh
package db

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	logging "github.com/op/go-logging"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var log = logging.MustGetLogger("db")

type Database interface {
	// migrate database schema
	Migrate(context.Context) error

	// close opened database
	Close() error

	ListUsers(context.Context) ([]*User, error)
	GetUser(context.Context, string) (*User, error)
	PutUser(context.Context, string, func(*User) error) (*User, error)

	ListNodes(context.Context) ([]*Node, error)
	GetNode(context.Context, string) (*Node, error)
	PutNode(context.Context, string, func(*Node) error) (*Node, error)
}

type Settings struct {
	// postgres connection settings
	Host         string
	Port         uint16
	User         string
	Password     string
	DatabaseName string
	SSLMode      string

	// optional peer tunnel settings, for reaching a remote postgres through
	// a peer node's ssh service port instead of connecting directly
	PeerAddress       string        // peer ssh address, empty = direct connection
	PeerSigner        ssh.Signer    // node certificate signer, required if PeerAddress is set
	PeerHostPublicKey ssh.PublicKey // pinned host public key of the peer, required if PeerAddress is set
}

type database struct {
	db        *gorm.DB
	waitGroup sync.WaitGroup
}

func Open(settings *Settings) (Database, error) {
	self := &database{}

	sslMode := settings.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		settings.Host, settings.Port, settings.User, settings.Password, settings.DatabaseName, sslMode,
	)

	var dialector gorm.Dialector
	if settings.PeerAddress != "" {
		// tunnel postgres through a peer node's ssh service port
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, err
		}
		// the DialFunc tunnels every connection, so skip the real DNS lookup
		// pgx would otherwise do for the configured host. keep config.Host
		// untouched so it remains the TLS server name for verify-ca/verify-full.
		config.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
			return []string{host}, nil
		}
		config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPostgresViaPeer(settings.PeerAddress, settings.PeerSigner, settings.PeerHostPublicKey, &self.waitGroup)
		}
		dialector = postgres.New(postgres.Config{
			Conn: stdlib.OpenDB(*config),
		})
	} else {
		dialector = postgres.Open(dsn)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.New(
			&logWriter{},
			logger.Config{
				SlowThreshold:             10 * time.Millisecond,
				LogLevel:                  logger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		),
	})
	if err != nil {
		return nil, err
	}
	self.db = db.Debug()
	return self, nil
}

func (self *database) Close() error {
	// closing the pool closes every pooled connection, which for the peer
	// tunnel mode closes the underlying ssh client via tunnelConn.Close
	sqlDatabase, err := self.db.DB()
	if err != nil {
		return err
	}
	if err := sqlDatabase.Close(); err != nil {
		return err
	}
	// wait for the per-tunnel request-draining goroutines to exit
	self.waitGroup.Wait()
	return nil
}

type logWriter struct {
}

func (self *logWriter) Printf(format string, arguments ...interface{}) {
	log.Debugf(format, arguments...)
}
