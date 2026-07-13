package auth

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	logging "github.com/op/go-logging"
	geoip2 "github.com/oschwald/geoip2-golang/v2"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/util/deferutil"
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
	CAPublicKeys []ssh.PublicKey

	// certificate authorities trusted for peer node certificates, empty
	// disables inbound peer connections
	PeerCAPublicKeys []ssh.PublicKey

	// id of this node, recorded on users at auth time for mesh tunneling
	NodeID string

	// path to the geoip database file
	GeoipDatabase string
}

// how long the administrator/disabled flags from the last database read are
// reused: repeat logins within this window do not wait on the cross-region
// login write, and flag changes in the database take up to this long to
// apply on this node (kickUser still removes a connected user immediately)
const userFlagsTimeToLive = time.Minute

func NewConfig(database db.Database, settings *Settings) (*ssh.ServerConfig, error) {
	authenticator := &authenticator{
		database:  database,
		settings:  settings,
		userFlags: make(map[string]userFlags),
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: authenticator.authenticate,
		AuthLogCallback:   authenticator.log,
	}
	return config, nil
}

// userFlags caches the account flags a login decision needs
type userFlags struct {
	administrator bool
	disabled      bool
	expiresAt     time.Time
}

type authenticator struct {
	database db.Database
	settings *Settings

	// per-user flags from the last database read, protected by userFlagsLock
	userFlags     map[string]userFlags
	userFlagsLock sync.Mutex
}

func (self *authenticator) getUserFlags(userId string) (userFlags, bool) {
	self.userFlagsLock.Lock()
	defer self.userFlagsLock.Unlock()

	flags, ok := self.userFlags[userId]
	if !ok || time.Now().After(flags.expiresAt) {
		return userFlags{}, false
	}
	return flags, true
}

func (self *authenticator) putUserFlags(userId string, user *db.User) {
	self.userFlagsLock.Lock()
	defer self.userFlagsLock.Unlock()

	self.userFlags[userId] = userFlags{
		administrator: user.Administrator,
		disabled:      user.Disabled,
		expiresAt:     time.Now().Add(userFlagsTimeToLive),
	}
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
			for _, caPublicKey := range self.settings.CAPublicKeys {
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

	// peer status must come only from authenticatePeer, never from a user
	// certificate extension: the user certificate authority must not be able
	// to grant node/peer capabilities.
	delete(permissions.Extensions, "peer")
	delete(permissions.Extensions, "identity")

	// update user in database, a single upsert so the authentication path
	// costs at most one database roundtrip; presence (node_id, online_at) is
	// only recorded for logins that will be accepted, a disabled user is
	// rejected right below and must not appear online
	userId := meta.User()
	flags, cached := self.getUserFlags(userId)
	if !cached {
		user, err := self.database.UpsertUserOnConnect(context.Background(), userId, ip.String(), location, self.settings.NodeID)
		if err != nil {
			return nil, err
		}
		self.putUserFlags(userId, user)
		flags = userFlags{administrator: user.Administrator, disabled: user.Disabled}
	}

	// check for deactivated user
	if flags.disabled {
		return nil, ErrInvalidCredentials
	}

	if cached {
		// flags from the recent database read decided the login above; the
		// login is recorded in the background so the session does not wait
		// on the cross-region write, and the fresh row rolls the cache over.
		// this runs only for accepted logins, so a rejection never writes
		// presence even when the cached flags have gone stale
		go func() {
			defer deferutil.Recover()
			user, err := self.database.UpsertUserOnConnect(context.Background(), userId, ip.String(), location, self.settings.NodeID)
			if err != nil {
				log.Warningf("failed to record login of user %q: %s", userId, err)
				return
			}
			self.putUserFlags(userId, user)
		}()
	}

	if flags.administrator {
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
	if len(self.settings.PeerCAPublicKeys) == 0 {
		log.Warningf("rejected peer connection from %s: no peer certificate authority configured", meta.RemoteAddr())
		return nil, ErrInvalidCredentials
	}

	certificateChecker := &ssh.CertChecker{
		IsUserAuthority: func(publicKey ssh.PublicKey) bool {
			for _, caPublicKey := range self.settings.PeerCAPublicKeys {
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
