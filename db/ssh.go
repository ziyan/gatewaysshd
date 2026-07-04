package db

import (
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/util/deferutil"
	"github.com/ziyan/gatewaysshd/util/sshconfig"
)

// ssh tunnel data structure, must match gateway/utils.go
type tunnelData struct {
	Host          string
	Port          uint32
	OriginAddress string
	OriginPort    uint32
}

// dialPostgresViaPeer creates a net.Conn to the remote postgres by opening a
// direct-tcpip channel to "postgres:5432" over a peer node's ssh service port.
func dialPostgresViaPeer(address string, signer ssh.Signer, pinnedHostKey ssh.PublicKey, waitGroup *sync.WaitGroup) (net.Conn, error) {
	config := sshconfig.NewPeerClientConfig(signer, pinnedHostKey)

	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, fmt.Errorf("db: failed to dial peer: %w", err)
	}

	channel, requests, err := client.OpenChannel("direct-tcpip", ssh.Marshal(&tunnelData{
		Host:          "postgres",
		Port:          5432,
		OriginAddress: "localhost",
		OriginPort:    0,
	}))
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: failed to open postgres tunnel: %w", err)
	}

	waitGroup.Add(1)
	go func() {
		defer deferutil.Recover()
		defer waitGroup.Done()
		ssh.DiscardRequests(requests)
	}()

	return &tunnelConn{
		channel: channel,
		client:  client,
	}, nil
}

// tunnelConn implements net.Conn over an ssh channel
type tunnelConn struct {
	channel ssh.Channel
	client  *ssh.Client
}

func (self *tunnelConn) Read(data []byte) (int, error)  { return self.channel.Read(data) }
func (self *tunnelConn) Write(data []byte) (int, error) { return self.channel.Write(data) }

func (self *tunnelConn) Close() error {
	_ = self.channel.Close()
	return self.client.Close()
}

// ssh channels do not support deadlines, so the deadline setters are no-ops.
// query-level context timeouts therefore cannot interrupt in-flight I/O over
// the tunnel; callers relying on them must set a connect timeout instead. This
// matches the transport being an already-authenticated, encrypted ssh channel.
func (self *tunnelConn) LocalAddr() net.Addr                       { return &net.TCPAddr{} }
func (self *tunnelConn) RemoteAddr() net.Addr                      { return &net.TCPAddr{} }
func (self *tunnelConn) SetDeadline(deadline time.Time) error      { return nil }
func (self *tunnelConn) SetReadDeadline(deadline time.Time) error  { return nil }
func (self *tunnelConn) SetWriteDeadline(deadline time.Time) error { return nil }
