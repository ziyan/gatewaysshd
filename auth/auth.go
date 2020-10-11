package auth

import (
	"bytes"
	"errors"
	"net"

	"github.com/op/go-logging"
	"github.com/oschwald/geoip2-golang"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
)

var log = logging.MustGetLogger("auth")

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
)

func NewConfig(database db.Database, caPublicKeys []ssh.PublicKey, geoipDatabase string) (*ssh.ServerConfig, error) {
	authenticator := &authenticator{
		database:      database,
		caPublicKeys:  caPublicKeys,
		geoipDatabase: geoipDatabase,
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: authenticator.authenticate,
		AuthLogCallback:   authenticator.log,
	}
	return config, nil
}

type authenticator struct {
	database      db.Database
	caPublicKeys  []ssh.PublicKey
	geoipDatabase string
}

func (self *authenticator) authenticate(meta ssh.ConnMetadata, publicKey ssh.PublicKey) (*ssh.Permissions, error) {
	// lookup location
	ip := meta.RemoteAddr().(*net.TCPAddr).IP
	location := self.lookupLocation(ip)

	// check certificate
	certificateChecker := &ssh.CertChecker{
		IsUserAuthority: func(publicKey ssh.PublicKey) bool {
			for _, caPublicKey := range self.caPublicKeys {
				if bytes.Compare(caPublicKey.Marshal(), publicKey.Marshal()) == 0 {
					return true
				}
			}
			log.Warningf("unknown authority: %v", publicKey)
			return false
		},
	}

	permissions, err := certificateChecker.Authenticate(meta, publicKey)
	log.Debugf("remote = %s, local = %s, public_key = %v, permissions = %v, err = %v", meta.RemoteAddr(), meta.LocalAddr(), publicKey, permissions, err)
	if err != nil {
		return nil, err
	}

	// update user in database
	user, err := self.database.PutUser(meta.User(), func(model *db.User) error {
		model.IP = ip.String()
		model.Location = location
		return nil
	})
	if err != nil {
		return nil, err
	}

	// check for deactivated user
	if user.Disabled {
		return nil, ErrInvalidCredentials
	}

	if user.Administrator {
		permissions.Extensions["administrator"] = ""
	}

	// successful auth
	log.Infof("successful, username = %s, extensions = %q", meta.User(), permissions.Extensions)
	return permissions, nil
}

func (self *authenticator) lookupLocation(ip net.IP) db.Location {
	var location db.Location
	d, err := geoip2.Open(self.geoipDatabase)
	if err != nil {
		return location
	}
	defer d.Close()

	r, err := d.City(ip)
	if err != nil {
		return location
	}

	location.Country = r.Country.IsoCode
	location.Latitude = r.Location.Latitude
	location.Longitude = r.Location.Longitude
	location.City = r.City.Names["en"]
	location.TimeZone = r.Location.TimeZone
	return location
}

func (self *authenticator) log(meta ssh.ConnMetadata, method string, err error) {
	log.Debugf("remote = %s, local = %s, user = %s, method = %s, error = %v", meta.RemoteAddr(), meta.LocalAddr(), meta.User(), method, err)
}
