// Package sshconfig keeps ssh crypto and peer client settings consistent
// across the application.
package sshconfig

import (
	"bytes"
	"fmt"
	"net"
	"time"

	logging "github.com/op/go-logging"
	"golang.org/x/crypto/ssh"
)

var log = logging.MustGetLogger("sshconfig") //nolint:unused

// PeerUser is the reserved username peer nodes authenticate with.
const PeerUser = "peer"

// CheckPinnedHostKey accepts the remote host key when it, or the key embedded
// in its certificate, matches the pinned public key. Peer nodes have host
// certificates signed by their own certificate authority which the dialing
// node does not necessarily trust, so the key itself is pinned.
func CheckPinnedHostKey(pinned ssh.PublicKey) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if certificate, ok := key.(*ssh.Certificate); ok {
			key = certificate.Key
		}
		if !bytes.Equal(key.Marshal(), pinned.Marshal()) {
			return fmt.Errorf("sshconfig: host key mismatch for peer %s", hostname)
		}
		return nil
	}
}

// NewPeerClientConfig builds the ssh client config used to authenticate to
// another node as a peer, with the hardened crypto settings applied.
func NewPeerClientConfig(signer ssh.Signer, pinnedHostKey ssh.PublicKey) *ssh.ClientConfig {
	config := &ssh.ClientConfig{
		User:            PeerUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: CheckPinnedHostKey(pinnedHostKey),
		Timeout:         10 * time.Second,
	}
	ApplySSHCryptoConfig(&config.Config)
	return config
}

// ApplySSHCryptoConfig removes insecure key exchanges, ciphers, and MACs from
// the default lists.
func ApplySSHCryptoConfig(config *ssh.Config) {
	config.KeyExchanges = []string{
		ssh.KeyExchangeCurve25519,
		ssh.KeyExchangeECDHP256,
		ssh.KeyExchangeECDHP384,
		ssh.KeyExchangeECDHP521,
		ssh.KeyExchangeDH14SHA256,
	}
	config.Ciphers = []string{
		ssh.CipherAES128GCM,
		ssh.CipherAES256GCM,
		ssh.CipherAES128CTR,
		ssh.CipherAES192CTR,
		ssh.CipherAES256CTR,
	}
	config.MACs = []string{
		ssh.HMACSHA256ETM,
		ssh.HMACSHA512ETM,
		ssh.HMACSHA256,
		ssh.HMACSHA512,
		ssh.HMACSHA1,
	}
}
