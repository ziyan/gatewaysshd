// Database
//
// authorities - store certificate authorities, private key, hostname regex
// group - store group, principal regex, principal validity
// user - store user special attributes, such as non-ldap user, groups
// certificates - store signed certificates, key by username/serial
//
package db

import (
	"fmt"
	"time"

	"github.com/op/go-logging"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var log = logging.MustGetLogger("db")

type Database interface {
	// migrate database schema
	Migrate() error

	// close opened database
	Close() error

	ListUsers() ([]*User, error)
	GetUser(string) (*User, error)
	PutUser(string, func(*User) error) (*User, error)
}

type database struct {
	db *gorm.DB
}

func Open(host string, port uint16, user, password, dbname string) (Database, error) {
	connection := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", host, port, user, password, dbname)
	db, err := gorm.Open(postgres.Open(connection), &gorm.Config{
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
	self := &database{
		db: db.Debug(),
	}
	return self, nil
}

func (self *database) Close() error {
	return nil
}

type logWriter struct {
}

func (self *logWriter) Printf(format string, arguments ...interface{}) {
	log.Debugf(format, arguments...)
}
