package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
)

var flags = []cli.Flag{
	&cli.StringFlag{
		Name:  "log-level",
		Value: "DEBUG",
		Usage: "log level",
	},
	&cli.StringFlag{
		Name:  "log-format",
		Value: "%{color}%{time:2006-01-02T15:04:05.000-07:00} [%{level:.4s}] [%{shortfile} %{shortfunc}] %{message}%{color:reset}",
		Usage: "log format",
	},
	&cli.StringFlag{
		Name:  "listen-debug",
		Value: "127.0.0.1:6080",
		Usage: "debug listen endpoint",
	},
	&cli.StringFlag{
		Name:  "listen-ssh",
		Value: ":2020",
		Usage: "ssh listen endpoint",
	},
	&cli.StringFlag{
		Name:  "listen-http",
		Value: "127.0.0.1:2080",
		Usage: "http listen endpoint",
	},
	&cli.StringFlag{
		Name:  "ca-public-key",
		Value: "id_rsa.ca.pub",
		Usage: "path to certificate authority public key",
	},
	&cli.StringFlag{
		Name:  "host-public-key",
		Value: "id_rsa.pub",
		Usage: "path to host public key or certificate",
	},
	&cli.StringFlag{
		Name:  "host-private-key",
		Value: "id_rsa",
		Usage: "path to host private key",
	},
	&cli.StringFlag{
		Name:  "server-version",
		Value: "SSH-2.0-gatewaysshd",
		Usage: "ssh server version string",
	},
	&cli.StringFlag{
		Name:  "idle-timeout",
		Value: "600s",
		Usage: "ssh connection idle timeout",
	},
	&cli.StringFlag{
		Name:  "geoip-database",
		Value: "geoip.mmdb",
		Usage: "path to the geoip database file",
	},
	&cli.StringFlag{
		Name:  "postgres-host",
		Value: "127.0.0.1",
		Usage: "host of postgres database",
	},
	&cli.UintFlag{
		Name:  "postgres-port",
		Value: 5432,
		Usage: "port of postgres database",
	},
	&cli.StringFlag{
		Name:  "postgres-user",
		Value: "gatewaysshd",
		Usage: "user to authenticate with postgres database",
	},
	&cli.StringFlag{
		Name:  "postgres-password",
		Value: "gatewaysshd",
		Usage: "password to authenticate with postgres database",
	},
	&cli.StringFlag{
		Name:  "postgres-dbname",
		Value: "gatewaysshd",
		Usage: "postgres database name",
	},
	&cli.StringFlag{
		Name:  "postgres-sslmode",
		Value: "disable",
		Usage: "postgres sslmode (disable, require, verify-ca, verify-full)",
	},
	&cli.StringFlag{
		Name:  "postgres-peer",
		Value: "",
		Usage: "peer node ssh address to tunnel postgres through, empty means direct connection",
	},
	&cli.StringFlag{
		Name:  "postgres-peer-host-public-key",
		Value: "",
		Usage: "path to the pinned host public key of the postgres peer, required with postgres-peer",
	},
	&cli.StringFlag{
		Name:  "node-id",
		Value: "",
		Usage: "unique id of this node in the mesh, empty disables node peering",
	},
	&cli.StringFlag{
		Name:  "node-address",
		Value: "",
		Usage: "ssh address where peer nodes can reach this node, empty means not dialable",
	},
	&cli.StringFlag{
		Name:  "node-certificate",
		Value: "",
		Usage: "path to the node certificate for the host private key, signed by the peer certificate authority with the \"peer\" principal, empty disables outbound peering",
	},
	&cli.StringFlag{
		Name:  "peer-ca-public-key",
		Value: "",
		Usage: "path to certificate authority public keys trusted for inbound peer nodes, empty rejects peer connections",
	},
}

// parsePublicKeys reads one or more public keys in authorized key format
func parsePublicKeys(path string) ([]ssh.PublicKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 - operator-supplied key path
	if err != nil {
		return nil, err
	}
	var publicKeys []ssh.PublicKey
	for len(raw) > 0 {
		publicKey, _, _, rest, err := ssh.ParseAuthorizedKey(raw)
		if err != nil {
			return nil, err
		}
		publicKeys = append(publicKeys, publicKey)
		raw = rest
	}
	return publicKeys, nil
}

// parseNodeSigner builds the signer used to authenticate to other nodes, from
// the host private key and the node certificate signed by the peer
// certificate authority
func parseNodeSigner(command *cli.Command) (ssh.Signer, error) {
	certificatePath := command.String("node-certificate")
	if certificatePath == "" {
		return nil, nil
	}

	hostPrivateKeyRaw, err := os.ReadFile(command.String("host-private-key"))
	if err != nil {
		return nil, err
	}
	hostPrivateKey, err := ssh.ParsePrivateKey(hostPrivateKeyRaw)
	if err != nil {
		return nil, err
	}

	certificateRaw, err := os.ReadFile(certificatePath) // #nosec G304 - operator-supplied cert path
	if err != nil {
		return nil, err
	}
	certificatePublicKey, _, _, _, err := ssh.ParseAuthorizedKey(certificateRaw)
	if err != nil {
		return nil, err
	}
	certificate, ok := certificatePublicKey.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("cli: node certificate file %q does not contain a certificate", certificatePath)
	}
	return ssh.NewCertSigner(certificate, hostPrivateKey)
}

func parseCaPublicKeys(command *cli.Command) ([]ssh.PublicKey, error) {
	return parsePublicKeys(command.String("ca-public-key"))
}

func parseHostSigner(command *cli.Command) (ssh.Signer, error) {
	// parse host private key
	hostPrivateKeyRaw, err := os.ReadFile(command.String("host-private-key"))
	if err != nil {
		return nil, err
	}
	hostPrivateKey, err := ssh.ParsePrivateKey(hostPrivateKeyRaw)
	if err != nil {
		return nil, err
	}

	// parse host certificate
	hostPublicKeyRaw, err := os.ReadFile(command.String("host-public-key"))
	if err != nil {
		return nil, err
	}
	hostPublicKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPublicKeyRaw)
	if err != nil {
		return nil, err
	}

	if hostCertificate, ok := hostPublicKey.(*ssh.Certificate); ok {
		hostSigner, err := ssh.NewCertSigner(hostCertificate, hostPrivateKey)
		if err != nil {
			return nil, err
		}
		return hostSigner, nil
	}
	return hostPrivateKey, nil
}

func parseIdleTimeout(command *cli.Command) (time.Duration, error) {
	return time.ParseDuration(command.String("idle-timeout"))
}
