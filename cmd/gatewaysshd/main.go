package main

import (
	"flag"
	"io/ioutil"
	"net"
	"runtime"

	"github.com/golang/glog"
	"github.com/ziyan/gatewaysshd/pkg/gateway"
)

var (
	LISTEN           = flag.String("listen", ":2222", "listen endpoint")
	HOST_PRIVATE_KEY = flag.String("host-private-key", "id_rsa.host", "path to host private key")
	CA_PUBLIC_KEY    = flag.String("ca-public-key", "id_rsa.ca.pub", "path to certificate authority public key")
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

	hostPrivateKey, err := ioutil.ReadFile(*HOST_PRIVATE_KEY)
	if err != nil {
		panic(err)
	}

	// create gateway
	gateway, err := gateway.NewGateway(caPublicKey, hostPrivateKey)
	if err != nil {
		panic(err)
	}

	// listen
	glog.Infof("listening on %s", *LISTEN)
	listener, err := net.Listen("tcp", *LISTEN)
	if err != nil {
		panic(err)
	}

	// Accept all connections
	for {
		tcp, err := listener.Accept()
		if err != nil {
			glog.Warningf("failed to accept incoming tcp connection: %s", err)
			continue
		}

		go gateway.HandleConnection(tcp)
	}
}
