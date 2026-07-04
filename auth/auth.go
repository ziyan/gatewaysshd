package auth

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"

	logging "github.com/op/go-logging"
	geoip2 "github.com/oschwald/geoip2-golang/v2"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
)

var log = logging.MustGetLogger("auth")

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
)

// PeerUser is the reserved username peer nodes authenticate with. Certificates
// presented for this user are checked against the peer certificate authorities
// instead of the user-facing ones, so inter-node trust can be layered on top of
// existing deployments without touching their user certificate authority.
const PeerUser = "peer"

type Settings struct {
	// certificate authorities trusted for regular user certificates
	CaPublicKeys []ssh.PublicKey

	// certificate authorities trusted for peer node certificates, empty
	// disables inbound peer connections
	PeerCaPublicKeys []ssh.PublicKey

	// id of this node, recorded on users at auth time for mesh tunneling
	NodeID string

	// path to the geoip database file
	GeoipDatabase string
}

func NewConfig(database db.Database, settings *Settings) (*ssh.ServerConfig, error) {
	authenticator := &authenticator{
		database: database,
		settings: settings,
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: authenticator.authenticate,
		AuthLogCallback:   authenticator.log,
	}
	return config, nil
}

type authenticator struct {
	database db.Database
	settings *Settings
}

func (self *authenticator) authenticate(meta ssh.ConnMetadata, publicKey ssh.PublicKey) (*ssh.Permissions, error) {
	// peer nodes use a reserved username and a separate certificate authority
	if meta.User() == PeerUser {
		return self.authenticatePeer(meta, publicKey)
	}

	// lookup location
	ip := meta.RemoteAddr().(*net.TCPAddr).AddrPort().Addr()
	location := self.lookupLocation(ip)

	// check certificate
	certificateChecker := &ssh.CertChecker{
		IsUserAuthority: func(publicKey ssh.PublicKey) bool {
			for _, caPublicKey := range self.settings.CaPublicKeys {
				if bytes.Equal(caPublicKey.Marshal(), publicKey.Marshal()) {
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
	user, err := self.database.PutUser(context.Background(), meta.User(), func(model *db.User) error {
		model.IP = ip.String()
		model.Location = location
		model.NodeID = self.settings.NodeID
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

// authenticatePeer validates a peer node certificate against the peer
// certificate authorities. The certificate must carry the "peer" principal and
// its key id identifies the node. Permissions are built fresh so certificate
// extensions never grant user-level capabilities.
func (self *authenticator) authenticatePeer(meta ssh.ConnMetadata, publicKey ssh.PublicKey) (*ssh.Permissions, error) {
	if len(self.settings.PeerCaPublicKeys) == 0 {
		log.Warningf("rejected peer connection from %s: no peer certificate authority configured", meta.RemoteAddr())
		return nil, ErrInvalidCredentials
	}

	certificateChecker := &ssh.CertChecker{
		IsUserAuthority: func(publicKey ssh.PublicKey) bool {
			for _, caPublicKey := range self.settings.PeerCaPublicKeys {
				if bytes.Equal(caPublicKey.Marshal(), publicKey.Marshal()) {
					return true
				}
			}
			log.Warningf("unknown peer authority: %v", publicKey)
			return false
		},
	}
	if _, err := certificateChecker.Authenticate(meta, publicKey); err != nil {
		return nil, err
	}

	certificate, ok := publicKey.(*ssh.Certificate)
	if !ok || certificate.KeyId == "" {
		return nil, ErrInvalidCredentials
	}

	log.Infof("successful peer authentication, node = %s, remote = %s", certificate.KeyId, meta.RemoteAddr())
	return &ssh.Permissions{
		Extensions: map[string]string{
			"peer":     "",
			"identity": certificate.KeyId,
		},
	}, nil
}

func (self *authenticator) lookupLocation(ip netip.Addr) db.Location {
	var location db.Location
	reader, err := geoip2.Open(self.settings.GeoipDatabase)
	if err != nil {
		return location
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warningf("failed to close geoip database: %v", err)
		}
	}()

	record, err := reader.City(ip)
	if err != nil {
		return location
	}

	location.Country = record.Country.ISOCode
	if record.Location.Latitude != nil {
		location.Latitude = *record.Location.Latitude
	}
	if record.Location.Longitude != nil {
		location.Longitude = *record.Location.Longitude
	}
	location.City = record.City.Names.English
	location.TimeZone = record.Location.TimeZone
	return location
}

func (self *authenticator) log(meta ssh.ConnMetadata, method string, err error) {
	log.Debugf("remote = %s, local = %s, user = %s, method = %s, error = %v", meta.RemoteAddr(), meta.LocalAddr(), meta.User(), method, err)
}
