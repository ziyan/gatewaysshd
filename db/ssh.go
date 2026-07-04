package db

import (
	"bytes"
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

// checkPinnedHostKey accepts the remote host key when it, or the key embedded
// in its certificate, matches the pinned public key. The peer's host
// certificate is signed by that node's own certificate authority which this
// node does not necessarily trust, so the key itself is pinned instead.
func checkPinnedHostKey(pinned ssh.PublicKey) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if certificate, ok := key.(*ssh.Certificate); ok {
			key = certificate.Key
		}
		if !bytes.Equal(key.Marshal(), pinned.Marshal()) {
			return fmt.Errorf("db: host key mismatch for peer %s", hostname)
		}
		return nil
	}
}

// dialPostgresViaPeer creates a net.Conn to the remote postgres by opening a
// direct-tcpip channel to "postgres:5432" over a peer node's ssh service port.
func dialPostgresViaPeer(address string, signer ssh.Signer, pinnedHostKey ssh.PublicKey, waitGroup *sync.WaitGroup) (net.Conn, error) {
	config := &ssh.ClientConfig{
		User: "peer",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: checkPinnedHostKey(pinnedHostKey),
		Timeout:         10 * time.Second,
	}
	sshconfig.ApplySSHCryptoConfig(&config.Config)

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

func (self *tunnelConn) LocalAddr() net.Addr                       { return &net.TCPAddr{} }
func (self *tunnelConn) RemoteAddr() net.Addr                      { return &net.TCPAddr{} }
func (self *tunnelConn) SetDeadline(deadline time.Time) error      { return nil }
func (self *tunnelConn) SetReadDeadline(deadline time.Time) error  { return nil }
func (self *tunnelConn) SetWriteDeadline(deadline time.Time) error { return nil }
