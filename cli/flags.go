package cli

import (
	"github.com/urfave/cli"
)

var flags = []cli.Flag{
	cli.StringFlag{
		Name:  "log-level",
		Value: "INFO",
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
		Name:  "host-certificate",
		Value: "id_rsa.host-cert.pub",
		Usage: "path to host certificate",
	},
	cli.StringFlag{
		Name:  "host-private-key",
		Value: "id_rsa.host",
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
