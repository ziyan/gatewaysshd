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

	"github.com/jinzhu/gorm"
	"github.com/op/go-logging"

	_ "github.com/jinzhu/gorm/dialects/postgres"
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
	db, err := gorm.Open("postgres", connection)
	if err != nil {
		return nil, err
	}
	self := &database{
		db: db.Debug(),
	}
	self.db.SetLogger(self)
	return self, nil
}

func (self *database) Close() error {
	return self.db.Close()
}

// for logging from gorm package
func (self *database) Print(values ...interface{}) {
	if len(values) < 2 {
		log.Debugf("%v", values)
		return
	}
	component := values[0].(string)
	filenameWithLine := values[1].(string)
	values = values[2:]
	switch component {
	case "sql":
		if len(values) != 4 {
			return
		}
		duration := values[0].(time.Duration)
		sql := values[1].(string)
		variables := values[2]
		rowsAffected := values[3].(int64)
		log.Debugf("took %s to execute sql %s, variables %v, affected %d rows, called from %s", duration, sql, variables, rowsAffected, filenameWithLine)
	case "error":
		if len(values) != 1 {
			return
		}
		err := values[0].(error)
		log.Errorf("%s, called from %s", err, filenameWithLine)
	case "log":
		log.Debugf("%v, called from %s", values, filenameWithLine)
	default:
		log.Debugf("%v", values)
	}
}
