package cli

import (
	"encoding/json"
	"io/ioutil"
	"net"
	"net/http"
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
			Name:  "http-listen",
			Value: ":2080",
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
		cli.StringFlag{
			Name:  "database",
			Value: "database.db",
			Usage: "path to database file",
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

		// open database
		database, err := gateway.OpenDatabase(c.String("database"))
		if err != nil {
			log.Errorf("failed to open database %s: %s", c.String("database"), err)
			return err
		}
		defer database.Close()

		// create gateway
		gateway, err := gateway.NewGateway(c.String("server-version"), caPublicKey, hostCertificate, hostPrivateKey, c.String("revocation-list"), c.String("geoip-database"), database)
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

		// signal to quit
		quit := false

		// accept all connections
		accepting := make(chan struct{})
		go func() {
			defer close(accepting)
			for {
				tcp, err := listener.Accept()
				if quit {
					return
				}
				if err != nil {
					log.Warningf("failed to accept incoming tcp connection: %s", err)
					break
				}
				go gateway.HandleConnection(tcp)
			}
		}()

		// serve http
		httping := make(chan struct{})
		go func() {
			defer close(httping)

			wrapHandler := func(handler func(*http.Request) (interface{}, error)) func(http.ResponseWriter, *http.Request) {
				return func(response http.ResponseWriter, request *http.Request) {
					result, err := handler(request)
					if err != nil {
						log.Errorf("failed to handle request: %s", err)
						http.Error(response, "500 internal server error", http.StatusInternalServerError)
						return
					}

					raw, err := json.Marshal(result)
					if err != nil {
						log.Errorf("failed to encode json: %s", err)
						http.Error(response, "500 internal server error", http.StatusInternalServerError)
						return
					}

					response.Header().Set("Content-Type", "application/json; charset=utf-8")
					response.Write(raw)
				}
			}

			mux := http.NewServeMux()
			mux.HandleFunc("/api/users", wrapHandler(func(request *http.Request) (interface{}, error) {
				return gateway.ListUsers()
			}))
			mux.HandleFunc("/api/connections", wrapHandler(func(request *http.Request) (interface{}, error) {
				return gateway.ListConnections()
			}))

			log.Noticef("listening on %s", c.String("http-listen"))
			err := http.ListenAndServe(c.String("http-listen"), mux)
			if quit {
				return
			}
			if err != nil {
				log.Warningf("http server exited with error: %s", err)
			}
		}()

		// wait till exit
		signaling := make(chan os.Signal, 1)
		signal.Notify(signaling, os.Interrupt)
		for !quit {
			select {
			case <-signaling:
				quit = true
			case <-accepting:
				quit = true
			case <-httping:
				quit = true
			case <-time.After(10 * time.Second):
				gateway.ScavengeConnections(idleTimeout)
			}
		}
		quit = true

		log.Noticef("exiting ...")
		return nil
	}

	app.Run(args)
}
