package main

import (
	"flag"
	"io/ioutil"
	"net"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/golang/glog"
	"github.com/ziyan/gatewaysshd/pkg/gateway"
)

var (
	LISTEN           = flag.String("listen", ":2020", "listen endpoint")
	CA_PUBLIC_KEY    = flag.String("ca-public-key", "id_rsa.ca.pub", "path to certificate authority public key")
	HOST_CERTIFICATE = flag.String("host-certificate", "id_rsa.host-cert.pub", "path to host certificate")
	HOST_PRIVATE_KEY = flag.String("host-private-key", "id_rsa.host", "path to host private key")
	SERVER_VERSION   = flag.String("server-version", "SSH-2.0-gatewaysshd", "ssh server version")
	IDLE_TIMEOUT     = flag.Uint64("idle-timeout", 600, "idle time out in seconds")
)

func main() {

	// use all CPU cores for maximum performance
	runtime.GOMAXPROCS(runtime.NumCPU())

	flag.Parse()

	// get the keys
	caPublicKey, err := ioutil.ReadFile(*CA_PUBLIC_KEY)
	if err != nil {
		panic(err)
	}

	hostCertificate, err := ioutil.ReadFile(*HOST_CERTIFICATE)
	if err != nil {
		panic(err)
	}

	hostPrivateKey, err := ioutil.ReadFile(*HOST_PRIVATE_KEY)
	if err != nil {
		panic(err)
	}

	// create gateway
	gateway, err := gateway.NewGateway(*SERVER_VERSION, caPublicKey, hostCertificate, hostPrivateKey)
	if err != nil {
		panic(err)
	}
	defer gateway.Close()

	// listen
	glog.Infof("listening on %s", *LISTEN)
	listener, err := net.Listen("tcp", *LISTEN)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			glog.Warningf("failed to close listener: %s", err)
		}
	}()

	// accept all connections
	go func() {
		for {
			tcp, err := listener.Accept()
			if err != nil {
				glog.Warningf("failed to accept incoming tcp connection: %s", err)
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
			gateway.ScavengeSessions(time.Duration(*IDLE_TIMEOUT) * time.Second)
		}
	}

	glog.Infof("exiting ...")
}
