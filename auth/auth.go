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
	ErrInvalidCertificate = errors.New("auth: invalid certificate")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
)

func NewConfig(database db.Database, caPublicKeys, hostCertificate, hostPrivateKey []byte, geoipDatabase string) (*ssh.ServerConfig, error) {
	authenticator := &authenticator{
		database:      database,
		geoipDatabase: geoipDatabase,
	}

	// parse certificate authority
	var cas []ssh.PublicKey
	for len(caPublicKeys) > 0 {
		ca, _, _, rest, err := ssh.ParseAuthorizedKey(caPublicKeys)
		if err != nil {
			return nil, err
		}
		log.Debugf("ca_public_key = %v", ca)
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
	log.Debugf("host_public_key = %v", key.PublicKey())

	// create checker
	certificateChecker := &ssh.CertChecker{
		IsUserAuthority: func(key ssh.PublicKey) bool {
			for _, ca := range cas {
				if bytes.Compare(ca.Marshal(), key.Marshal()) == 0 {
					return true
				}
			}
			log.Warningf("unknown authority: %v", key)
			return false
		},
	}

	// create server config
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			permissions, err := certificateChecker.Authenticate(meta, key)
			log.Debugf("remote = %s, local = %s, public_key = %v, permissions = %v, err = %v", meta.RemoteAddr(), meta.LocalAddr(), key, permissions, err)
			if err != nil {
				return nil, err
			}
			return authenticator.authenticate(meta.User(), permissions, meta.RemoteAddr().(*net.TCPAddr).IP)
		},
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			log.Debugf("remote = %s, local = %s, user = %s, method = %s, error = %v", meta.RemoteAddr(), meta.LocalAddr(), meta.User(), method, err)
		},
	}
	config.AddHostKey(host)
	return config, nil
}

type authenticator struct {
	database      db.Database
	geoipDatabase string
}

func (self *authenticator) authenticate(username string, permissions *ssh.Permissions, ip net.IP) (*ssh.Permissions, error) {
	// lookup location
	location := self.lookupLocation(ip)

	// update user in database
	user, err := self.database.PutUser(username, func(model *db.User) error {
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

	// successful auth
	log.Infof("successful, username = %s, extensions = %q", username, permissions.Extensions)
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
