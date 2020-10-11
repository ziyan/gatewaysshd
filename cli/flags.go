package cli

import (
	"io/ioutil"
	"time"

	"github.com/urfave/cli"
	"golang.org/x/crypto/ssh"
)

var flags = []cli.Flag{
	cli.StringFlag{
		Name:  "log-level",
		Value: "DEBUG",
		Usage: "log level",
	},
	cli.StringFlag{
		Name:  "log-format",
		Value: "%{color}%{time:2006-01-02T15:04:05.000-07:00} [%{level:.4s}] [%{shortfile} %{shortfunc}] %{message}%{color:reset}",
		Usage: "log format",
	},
	cli.StringFlag{
		Name:  "listen-ssh",
		Value: ":2020",
		Usage: "ssh listen endpoint",
	},
	cli.StringFlag{
		Name:  "listen-http",
		Value: "",
		Usage: "http listen endpoint",
	},
	cli.StringFlag{
		Name:  "ca-public-key",
		Value: "id_rsa.ca.pub",
		Usage: "path to certificate authority public key",
	},
	cli.StringFlag{
		Name:  "host-public-key",
		Value: "id_rsa.pub",
		Usage: "path to host public key or certificate",
	},
	cli.StringFlag{
		Name:  "host-private-key",
		Value: "id_rsa",
		Usage: "path to host private key",
	},
	cli.StringFlag{
		Name:  "server-version",
		Value: "SSH-2.0-gatewaysshd",
		Usage: "ssh server version string",
	},
	cli.StringFlag{
		Name:  "idle-timeout",
		Value: "600s",
		Usage: "ssh connection idle timeout",
	},
	cli.StringFlag{
		Name:  "geoip-database",
		Value: "geoip.mmdb",
		Usage: "path to the geoip database file",
	},
	cli.StringFlag{
		Name:  "postgres-host",
		Value: "127.0.0.1",
		Usage: "host of postgres database",
	},
	cli.UintFlag{
		Name:  "postgres-port",
		Value: 5432,
		Usage: "port of postgres database",
	},
	cli.StringFlag{
		Name:  "postgres-user",
		Value: "gatewaysshd",
		Usage: "user to authenticate with postgres database",
	},
	cli.StringFlag{
		Name:  "postgres-password",
		Value: "gatewaysshd",
		Usage: "password to authenticate with postgres database",
	},
	cli.StringFlag{
		Name:  "postgres-dbname",
		Value: "gatewaysshd",
		Usage: "postgres database name",
	},
	cli.BoolFlag{
		Name:  "debug-pprof",
		Usage: "enable pprof debugging",
	},
}

func parseCaPublicKeys(c *cli.Context) ([]ssh.PublicKey, error) {
	// get the keys
	caPublicKeyRaw, err := ioutil.ReadFile(c.String("ca-public-key"))
	if err != nil {
		return nil, err
	}
	var caPublicKeys []ssh.PublicKey
	for len(caPublicKeyRaw) > 0 {
		caPublicKey, _, _, rest, err := ssh.ParseAuthorizedKey(caPublicKeyRaw)
		if err != nil {
			return nil, err
		}
		caPublicKeys = append(caPublicKeys, caPublicKey)
		caPublicKeyRaw = rest
	}
	return caPublicKeys, nil
}

func parseHostSigner(c *cli.Context) (ssh.Signer, error) {
	// parse host private key
	hostPrivateKeyRaw, err := ioutil.ReadFile(c.String("host-private-key"))
	if err != nil {
		return nil, err
	}
	hostPrivateKey, err := ssh.ParsePrivateKey(hostPrivateKeyRaw)
	if err != nil {
		return nil, err
	}

	// parse host certificate
	hostPublicKeyRaw, err := ioutil.ReadFile(c.String("host-public-key"))
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

func parseIdleTimeout(c *cli.Context) (time.Duration, error) {
	return time.ParseDuration(c.String("idle-timeout"))
}
