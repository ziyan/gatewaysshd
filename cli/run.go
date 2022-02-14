package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/urfave/cli"

	"github.com/ziyan/gatewaysshd/auth"
	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/gateway"
)

func run(c *cli.Context) error {
	caPublicKeys, err := parseCaPublicKeys(c)
	if err != nil {
		log.Errorf("failed to parse certificate authority public key from file \"%s\": %s", c.String("ca-public-key"), err)
		return err
	}

	hostSigner, err := parseHostSigner(c)
	if err != nil {
		log.Errorf("failed to host key from file \"%s\" and \"%s\": %s", c.String("host-private-key"), c.String("host-public-key"), err)
		return err
	}

	idleTimeout, err := parseIdleTimeout(c)
	if err != nil {
		log.Errorf("failed to parse idle timeout \"%s\": %s", c.String("idle-timeout"), err)
		return err
	}

	// ssh listener
	log.Debugf("listening ssh endpoint: %s", c.String("listen-ssh"))
	sshListener, err := net.Listen("tcp", c.String("listen-ssh"))
	if err != nil {
		log.Errorf("failed to listen on %s: %s", c.String("listen-ssh"), err)
		return err
	}

	// http listener
	var httpListener net.Listener
	if c.String("listen-http") != "" {
		log.Debugf("listening http endpoint: %s", c.String("listen-http"))
		httpListener, err = net.Listen("tcp", c.String("listen-http"))
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
	sshConfig, err := auth.NewConfig(database, caPublicKeys, c.String("geoip-database"))
	if err != nil {
		log.Errorf("failed to create ssh config: %s", err)
		return err
	}
	sshConfig.ServerVersion = c.String("server-version")
	sshConfig.AddHostKey(hostSigner)

	// create gateway
	gateway, err := gateway.Open(database, sshConfig, &gateway.Settings{
		Version: c.App.Version,
	})
	if err != nil {
		log.Errorf("failed to create ssh gateway: %s", err)
		return err
	}
	defer gateway.Close()

	// run
	var waitGroup sync.WaitGroup

	// accept all connections
	sshRunning := make(chan struct{})
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		defer close(sshRunning)

		log.Debugf("running and serving ssh")
		for {
			socket, err := sshListener.Accept()
			if err != nil {
				log.Errorf("failed to accept incoming tcp connection: %s", err)
				break
			}
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				gateway.HandleConnection(socket)
			}()
		}

		log.Debugf("stop serving ssh")
	}()

	// serve http
	var httpServer *http.Server
	httpRunning := make(chan struct{})
	if c.String("listen-http") != "" {
		httpServer = &http.Server{
			Addr:    c.String("listen-http"),
			Handler: newHttpHandler(gateway, c.Bool("debug-pprof")),
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
	signaling := make(chan os.Signal, 2)
	signal.Notify(signaling, syscall.SIGINT, syscall.SIGTERM)

	// signal to quit
	quit := false
	for !quit {
		select {
		case sig := <-signaling:
			log.Warningf("received signal %v", sig)
			quit = true
		case <-sshRunning:
			quit = true
		case <-httpRunning:
			quit = true
		case <-time.After(10 * time.Second):
			gateway.ScavengeConnections(idleTimeout)
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
		gateway.Close()
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

	waitGroup.Wait()
	return nil
}
