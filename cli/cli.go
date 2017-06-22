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
	app.Name = "SSH Gateway"
	app.Version = "0.1.0"
	app.Usage = "SSH Gateway"

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "loglevel",
			Value:  "INFO",
			Usage:  "Log level",
			EnvVar: "GATEWAYSSHD_LOG_LEVEL",
		},
		cli.StringFlag{
			Name:   "logformat",
			Value:  "%{color}%{time:2006-01-02T15:04:05.000Z07:00} [%{level:.4s}] [%{shortfile} %{shortfunc}] %{message}%{color:reset}",
			Usage:  "Log format",
			EnvVar: "GATEWAYSSHD_LOG_LEVEL",
		},
		cli.StringFlag{
			Name:   "listen",
			Value:  ":2020",
			Usage:  "Listen endpoint",
			EnvVar: "GATEWAYSSHD_LISTEN",
		},
		cli.StringFlag{
			Name:   "ca",
			Value:  "id_rsa.ca.pub",
			Usage:  "Path to certificate authority public key",
			EnvVar: "GATEWAYSSHD_CA_PUBLIC_KEY",
		},
		cli.StringFlag{
			Name:   "cert",
			Value:  "id_rsa.host-cert.pub",
			Usage:  "Path to host certificate",
			EnvVar: "GATEWAYSSHD_HOST_CERTIFICATE",
		},
		cli.StringFlag{
			Name:   "key",
			Value:  "id_rsa.host",
			Usage:  "Path to host private key",
			EnvVar: "GATEWAYSSHD_HOST_PRIVATE_KEY",
		},
		cli.StringFlag{
			Name:   "serverversion",
			Value:  "SSH-2.0-gatewaysshd",
			Usage:  "Server version string",
			EnvVar: "GATEWAYSSHD_SERVER_VERSION",
		},
		cli.IntFlag{
			Name:   "idletimeout",
			Value:  600,
			Usage:  "Idle timeout in seconds",
			EnvVar: "GATEWAYSSHD_IDLE_TIMEOUT",
		},
	}

	app.Action = func(c *cli.Context) error {
		configureLogging(c.String("loglevel"), c.String("logformat"))

		// get the keys
		caPublicKey, err := ioutil.ReadFile(c.String("ca"))
		if err != nil {
			log.Errorf("failed to load certificate authority public key from file \"%s\": %s", c.String("ca"), err)
			return err
		}

		hostCertificate, err := ioutil.ReadFile(c.String("cert"))
		if err != nil {
			log.Errorf("failed to load host certificate from file \"%s\": %s", c.String("cert"), err)
			return err
		}

		hostPrivateKey, err := ioutil.ReadFile(c.String("key"))
		if err != nil {
			log.Errorf("failed to load host private key from file \"%s\": %s", c.String("key"), err)
			return err
		}

		// create gateway
		gateway, err := gateway.NewGateway(c.String("serverversion"), caPublicKey, hostCertificate, hostPrivateKey)
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
			case <-time.After(30 * time.Second):
				gateway.ScavengeSessions(time.Duration(c.Int("idletimeout")) * time.Second)
			}
		}

		log.Noticef("exiting ...")
		return nil
	}

	app.Run(args)
}
