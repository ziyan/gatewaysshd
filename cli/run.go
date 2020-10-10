package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cloudflare/tableflip"
	"github.com/urfave/cli"

	"github.com/ziyan/gatewaysshd/auth"
	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/gateway"
)

func run(c *cli.Context) error {
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

	// set up upgrader
	upgrader, err := tableflip.New(tableflip.Options{
		PIDFile: c.String("pid-file"),
	})
	if err != nil {
		log.Errorf("failed to create upgrader: %s", err)
		return err
	}
	defer upgrader.Stop()

	// ssh listener
	log.Debugf("listening ssh endpoint: %s", c.String("listen-ssh"))
	sshListener, err := upgrader.Fds.Listen("tcp", c.String("listen-ssh"))
	if err != nil {
		log.Errorf("failed to listen on %s: %s", c.String("listen-ssh"), err)
		return err
	}

	// http listener
	var httpListener net.Listener
	if c.String("listen-http") != "" {
		log.Debugf("listening http endpoint: %s", c.String("listen-http"))
		httpListener, err = upgrader.Fds.Listen("tcp", c.String("listen-http"))
		if err != nil {
			log.Errorf("failed to listen on %s: %s", c.String("listen-http"), err)
			return err
		}
	}

	// open database
	database, err := db.Open(c.String("postgres-host"), uint16(c.Uint("postgres-port")), c.String("postgres-user"), c.String("postgres-password"), c.String("postgres-dbname"))
	if err != nil {
		log.Errorf("failed to open database: %s", err)
		return err
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Errorf("failed to close database: %s", err)
		}
	}()
	if err := database.Migrate(); err != nil {
		log.Errorf("failed to migrate database: %s", err)
		return err
	}

	// create ssh auth config
	sshConfig, err := auth.NewConfig(database, caPublicKey, hostCertificate, hostPrivateKey, c.String("geoip-database"))
	if err != nil {
		log.Errorf("failed to create ssh config: %s", err)
		return err
	}
	sshConfig.ServerVersion = c.String("server-version")

	// create gateway
	gateway, err := gateway.Open(database, sshConfig)
	if err != nil {
		log.Errorf("failed to create ssh gateway: %s", err)
		return err
	}
	defer gateway.Close()

	// tell parent that we are ready
	// need to get all the inheritted fds before calling this
	if err := upgrader.Ready(); err != nil {
		log.Errorf("failed to send ready to parent: %s", err)
		return err
	}

	// wait until parent exit before continuing
	if err := upgrader.WaitForParent(context.Background()); err != nil {
		log.Errorf("failed to wait for parent to shutdown: %s", err)
		return err
	}

	// run
	var waitGroup sync.WaitGroup
	defer waitGroup.Wait()

	// signal to quit
	quit := false

	// accept all connections
	sshRunning := make(chan struct{})
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		defer close(sshRunning)

		log.Debugf("running and serving ssh")
		for {
			socket, err := sshListener.Accept()
			if quit {
				break
			}
			if err != nil {
				log.Errorf("failed to accept incoming tcp connection: %s", err)
				break
			}
			go gateway.HandleConnection(socket)
		}

		log.Debugf("stop serving ssh")
	}()

	// serve http
	var httpServer *http.Server
	httpRunning := make(chan struct{})
	if c.String("listen-http") != "" {
		ErrNotFound := errors.New("404: not found")

		wrapHandler := func(handler func(*http.Request) (interface{}, error)) func(http.ResponseWriter, *http.Request) {
			return func(response http.ResponseWriter, request *http.Request) {
				result, err := handler(request)
				if err != nil {
					if err == ErrNotFound {
						http.NotFound(response, request)
						return
					}
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
		mux.HandleFunc("/api/user", wrapHandler(func(request *http.Request) (interface{}, error) {
			return gateway.ListUsers()
		}))
		mux.HandleFunc("/api/user/", wrapHandler(func(request *http.Request) (interface{}, error) {
			parts := strings.Split(request.URL.Path, "/")
			if len(parts) != 4 {
				return nil, ErrNotFound
			}
			user, err := gateway.GetUser(parts[3])
			if err != nil {
				return nil, err
			}
			if user == nil {
				return nil, ErrNotFound
			}
			return user, nil
		}))

		if c.Bool("debug-pprof") {
			mux.HandleFunc("/debug/pprof/", pprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		}
		httpServer = &http.Server{
			Addr:    c.String("listen-http"),
			Handler: mux,
		}
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			defer close(httpRunning)

			log.Debugf("running and serving http")
			if err := httpServer.Serve(httpListener); err != nil && err != http.ErrServerClosed {
				log.Errorf("http server exited with error: %s", err)
			}

			log.Debugf("stop serving http")
		}()
	}

	// wait till exit
	signaling := make(chan os.Signal, 1)
	signal.Notify(signaling, syscall.SIGINT, syscall.SIGTERM)
	upgrading := make(chan os.Signal, 1)
	signal.Notify(upgrading, syscall.SIGHUP)

	for !quit {
		select {
		case <-upgrading:
			log.Noticef("upgrading ...")
			if err := upgrader.Upgrade(); err != nil {
				log.Errorf("failed to upgrade: %s", err)
			}
		case <-signaling:
			quit = true
		case <-sshRunning:
			quit = true
		case <-httpRunning:
			quit = true
		case <-time.After(10 * time.Second):
			gateway.ScavengeConnections(idleTimeout)
		case <-upgrader.Exit():
			quit = true
		}
	}

	// make sure there is a deadline on shutting down
	time.AfterFunc(30*time.Second, func() {
		log.Fatalf("graceful shutdown server timed out")
		os.Exit(1)
	})

	// shutdown server, this will block until all connections are closed
	log.Noticef("shutting down")

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		if err := sshListener.Close(); err != nil {
			log.Errorf("failed to close ssh listener: %s", err)
		}
	}()

	if httpServer != nil {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := httpServer.Shutdown(context.Background()); err != nil {
				log.Errorf("failed to shutdown http server: %s", err)
			}
		}()
	}

	return nil
}
