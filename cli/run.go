package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/auth"
	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/gateway"
	"github.com/ziyan/gatewaysshd/util/debugutil"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

func run(ctx context.Context, command *cli.Command) error {
	// debugging endpoint, start as early as possible
	if command.String("listen-debug") != "" {
		stopDebugServer, err := debugutil.RunDebugServer(command.String("listen-debug"))
		if err != nil {
			return err
		}
		defer stopDebugServer()
	}

	caPublicKeys, err := parseCaPublicKeys(command)
	if err != nil {
		log.Errorf("failed to parse certificate authority public key from file \"%s\": %s", command.String("ca-public-key"), err)
		return err
	}

	hostSigner, err := parseHostSigner(command)
	if err != nil {
		log.Errorf("failed to host key from file \"%s\" and \"%s\": %s", command.String("host-private-key"), command.String("host-public-key"), err)
		return err
	}

	idleTimeout, err := parseIdleTimeout(command)
	if err != nil {
		log.Errorf("failed to parse idle timeout \"%s\": %s", command.String("idle-timeout"), err)
		return err
	}

	// ssh listener
	log.Debugf("listening ssh endpoint: %s", command.String("listen-ssh"))
	sshListener, err := net.Listen("tcp", command.String("listen-ssh"))
	if err != nil {
		log.Errorf("failed to listen on %s: %s", command.String("listen-ssh"), err)
		return err
	}

	// http listener
	var httpListener net.Listener
	if command.String("listen-http") != "" {
		log.Debugf("listening http endpoint: %s", command.String("listen-http"))
		httpListener, err = net.Listen("tcp", command.String("listen-http"))
		if err != nil {
			log.Errorf("failed to listen on %s: %s", command.String("listen-http"), err)
			return err
		}
	}

	// open database
	pgPort := command.Uint("postgres-port")
	if pgPort > 65535 {
		return fmt.Errorf("cli: postgres port %d is out of range (max 65535)", pgPort)
	}
	database, err := db.Open(command.String("postgres-host"), uint16(pgPort), command.String("postgres-user"), command.String("postgres-password"), command.String("postgres-dbname"))
	if err != nil {
		log.Errorf("failed to open database: %s", err)
		return err
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Errorf("failed to close database: %s", err)
		}
	}()
	if err := database.Migrate(ctx); err != nil {
		log.Errorf("failed to migrate database: %s", err)
		return err
	}

	// create ssh auth config
	sshConfig, err := auth.NewConfig(database, caPublicKeys, command.String("geoip-database"))
	if err != nil {
		log.Errorf("failed to create ssh config: %s", err)
		return err
	}
	// remove insecure key exchanges from the default list of key exchanges (ssh.defaultKexAlgos)
	sshConfig.KeyExchanges = []string{
		ssh.KeyExchangeCurve25519,
		ssh.KeyExchangeECDHP256,
		ssh.KeyExchangeECDHP384,
		ssh.KeyExchangeECDHP521,
		ssh.KeyExchangeDH14SHA256,
	}
	// remove insecure ciphers from the default list of ciphers (ssh.defaultCiphers)
	sshConfig.Ciphers = []string{
		ssh.CipherAES128GCM,
		ssh.CipherAES256GCM,
		ssh.CipherAES128CTR,
		ssh.CipherAES192CTR,
		ssh.CipherAES256CTR,
	}
	// remove insecure MACs from the default list of MACs (ssh.defaultMACs)
	sshConfig.MACs = []string{
		ssh.HMACSHA256ETM,
		ssh.HMACSHA512ETM,
		ssh.HMACSHA256,
		ssh.HMACSHA512,
		ssh.HMACSHA1,
	}
	sshConfig.ServerVersion = command.String("server-version")
	sshConfig.AddHostKey(hostSigner)

	// create gateway
	gateway, err := gateway.Open(database, sshConfig, &gateway.Settings{
		Version: command.Root().Version,
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
		defer deferutil.Recover()
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
				defer deferutil.Recover()
				defer waitGroup.Done()
				gateway.HandleConnection(socket)
			}()
		}

		log.Debugf("stop serving ssh")
	}()

	// serve http
	var httpServer *http.Server
	httpRunning := make(chan struct{})
	if command.String("listen-http") != "" {
		httpServer = &http.Server{
			Addr:              command.String("listen-http"),
			Handler:           newHttpHandler(gateway),
			ReadHeaderTimeout: 30 * time.Second,
		}
		waitGroup.Add(1)
		go func() {
			defer deferutil.Recover()
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
		case receivedSignal := <-signaling:
			log.Warningf("received signal %v", receivedSignal)
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
		defer deferutil.Recover()
		defer waitGroup.Done()
		if err := sshListener.Close(); err != nil {
			log.Errorf("failed to close ssh listener: %s", err)
		}
		gateway.Close()
	}()

	if httpServer != nil {
		waitGroup.Add(1)
		go func() {
			defer deferutil.Recover()
			defer waitGroup.Done()
			if err := httpServer.Shutdown(ctx); err != nil {
				log.Errorf("failed to shutdown http server: %s", err)
			}
		}()
	}

	waitGroup.Wait()
	return nil
}
