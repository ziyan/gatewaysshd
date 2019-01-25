package cli

import (
	"io/ioutil"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/op/go-logging"
	"github.com/urfave/cli"

	"github.com/ziyan/gatewaysshd/gateway"
)

var log = logging.MustGetLogger("cli")

func configureLogging(level, format string) {
	logging.SetBackend(logging.NewBackendFormatter(
		logging.NewLogBackend(os.Stderr, "", 0),
		logging.MustStringFormatter(format),
	))
	if level, err := logging.LogLevel(level); err == nil {
		logging.SetLevel(level, "")
	}
	log.Debugf("log level set to %s", logging.GetLevel(""))
}

func Run(args []string) {

	app := cli.NewApp()
	app.EnableBashCompletion = true
	app.Name = "gatewaysshd"
	app.Version = "0.1.0"
	app.Usage = "A daemon that provides a meeting place for all your SSH tunnels."

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "log-level",
			Value: "INFO",
			Usage: "log level",
		},
		cli.StringFlag{
			Name:  "log-format",
			Value: "%{color}%{time:2006-01-02T15:04:05.000Z07:00} [%{level:.4s}] [%{shortfile} %{shortfunc}] %{message}%{color:reset}",
			Usage: "log format",
		},
		cli.StringFlag{
			Name:  "listen",
			Value: ":2020",
			Usage: "listen endpoint",
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
			Usage: "server version string",
		},
		cli.StringFlag{
			Name:  "revocation-list",
			Value: "crl.txt",
			Usage: "a file containing one certificate key id per line for revoked certificates",
		},
		cli.StringFlag{
			Name:  "idle-timeout",
			Value: "600s",
			Usage: "idle timeout",
		},
		cli.StringFlag{
			Name:  "geoip-database",
			Value: "geoip.mmdb",
			Usage: "path to the geoip database file",
		},
	}

	app.Action = func(c *cli.Context) error {
		configureLogging(c.String("log-level"), c.String("log-format"))

		// get the keys
		caPublicKey, err := ioutil.ReadFile(c.String("ca-public-key"))
		if err != nil {
			log.Errorf("failed to load certificate authority public key from file \"%s\": %s", c.String("ca-public-key"), err)
			return err
		}

		hostCertificate, err := ioutil.ReadFile(c.String("host-certificate"))
		if err != nil {
			log.Errorf("failed to load host certificate from file \"%s\": %s", c.String("certificate"), err)
			return err
		}

		hostPrivateKey, err := ioutil.ReadFile(c.String("host-private-key"))
		if err != nil {
			log.Errorf("failed to load host private key from file \"%s\": %s", c.String("host-private-key"), err)
			return err
		}

		idleTimeout, err := time.ParseDuration(c.String("idle-timeout"))
		if err != nil {
			log.Errorf("failed to parse idle timeout \"%s\": %s", c.String("idle-timeout"), err)
			return err
		}

		// create gateway
		gateway, err := gateway.NewGateway(c.String("server-version"), caPublicKey, hostCertificate, hostPrivateKey, c.String("revocation-list"), c.String("geoip-database"))
		if err != nil {
			log.Errorf("failed to create ssh gateway: %s", err)
			return err
		}
		defer gateway.Close()

		// listen
		log.Noticef("listening on %s", c.String("listen"))
		listener, err := net.Listen("tcp", c.String("listen"))
		if err != nil {
			log.Errorf("failed to listen on \"%s\": %s", c.String("listen"), err)
			return err
		}
		defer func() {
			if err := listener.Close(); err != nil {
				log.Warningf("failed to close listener: %s", err)
			}
		}()

		// accept all connections
		go func() {
			for {
				tcp, err := listener.Accept()
				if err != nil {
					log.Warningf("failed to accept incoming tcp connection: %s", err)
					continue
				}
				go gateway.HandleConnection(tcp)
			}
		}()

		// wait till exit
		signaling := make(chan os.Signal, 1)
		signal.Notify(signaling, os.Interrupt)
		quit := false
		for !quit {
			select {
			case <-signaling:
				quit = true
			case <-time.After(10 * time.Second):
				gateway.ScavengeConnections(idleTimeout)
			}
		}

		log.Noticef("exiting ...")
		return nil
	}

	app.Run(args)
}
