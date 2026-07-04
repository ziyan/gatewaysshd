// Package sshconfig keeps ssh crypto settings consistent across the application.
package sshconfig

import (
	logging "github.com/op/go-logging"
	"golang.org/x/crypto/ssh"
)

var log = logging.MustGetLogger("sshconfig") //nolint:unused

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
