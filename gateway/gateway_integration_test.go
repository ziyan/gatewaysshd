package gateway_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/auth"
	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/db/dbtest"
	"github.com/ziyan/gatewaysshd/gateway"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

func newTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %s", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %s", err)
	}
	return signer
}

// newCertSigner returns a signer whose public key is a user certificate for
// the given principal, signed by the certificate authority.
func newCertSigner(t *testing.T, caSigner ssh.Signer, user string, extensions map[string]string) ssh.Signer {
	t.Helper()
	userSigner := newTestSigner(t)
	certificate := &ssh.Certificate{
		Key:             userSigner.PublicKey(),
		CertType:        ssh.UserCert,
		KeyId:           user,
		ValidPrincipals: []string{user},
		ValidAfter:      0,
		ValidBefore:     ssh.CertTimeInfinity,
		Permissions: ssh.Permissions{
			Extensions: extensions,
		},
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("failed to sign certificate: %s", err)
	}
	certSigner, err := ssh.NewCertSigner(certificate, userSigner)
	if err != nil {
		t.Fatalf("failed to create cert signer: %s", err)
	}
	return certSigner
}

// startTestGateway starts a gateway backed by a fresh test database on a real
// tcp listener and returns its address, the certificate authority signer and a
// cleanup func.
func startTestGateway(t *testing.T) (string, ssh.Signer, gateway.Gateway, db.Database, func()) {
	t.Helper()

	database, releaseDatabase := dbtest.AcquireDatabase(t)

	caSigner := newTestSigner(t)
	sshConfig, err := auth.NewConfig(database, []ssh.PublicKey{caSigner.PublicKey()}, "missing-geoip.mmdb")
	if err != nil {
		t.Fatalf("failed to create ssh config: %s", err)
	}
	sshConfig.AddHostKey(newTestSigner(t))

	instance, err := gateway.Open(database, sshConfig, &gateway.Settings{Version: "test"})
	if err != nil {
		t.Fatalf("failed to open gateway: %s", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	go func() {
		defer deferutil.Recover()
		for {
			socket, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer deferutil.Recover()
				instance.HandleConnection(socket)
			}()
		}
	}()

	return listener.Addr().String(), caSigner, instance, database, func() {
		if err := listener.Close(); err != nil {
			t.Fatalf("failed to close listener: %s", err)
		}
		instance.Close()
		releaseDatabase()
	}
}

func dialTestGateway(t *testing.T, address string, certSigner ssh.Signer, user string) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(certSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to dial gateway as %s: %s", user, err)
	}
	return client
}

func TestGatewayCertificateAuthentication(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	// a key that is not signed by the certificate authority must be rejected
	if _, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            "mallory",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(newTestSigner(t))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}); err == nil {
		t.Fatal("expected authentication to fail without certificate")
	}

	// a certificate signed by the certificate authority must be accepted
	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	// authentication must have created the user in the database
	user, err := instance.GetUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("failed to get user: %s", err)
	}
	if user == nil {
		t.Fatal("expected user alice to be created in database")
	}
}

func TestGatewayPingCommand(t *testing.T) {
	t.Parallel()
	address, caSigner, _, _, release := startTestGateway(t)
	defer release()

	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	defer func() {
		_ = session.Close()
	}()

	output, err := session.Output("ping")
	if err != nil {
		t.Fatalf("failed to run ping: %s", err)
	}
	if !strings.Contains(string(output), `"version":"test"`) {
		t.Fatalf("unexpected ping output: %s", output)
	}
}

func TestGatewayTunnel(t *testing.T) {
	t.Parallel()
	address, caSigner, _, _, release := startTestGateway(t)
	defer release()

	extensions := map[string]string{"permit-port-forwarding": ""}

	// alice advertises an echo service through the gateway
	alice := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()

	forwarded := alice.HandleChannelOpen("forwarded-tcpip")
	go func() {
		defer deferutil.Recover()
		for newChannel := range forwarded {
			channel, requests, err := newChannel.Accept()
			if err != nil {
				return
			}
			go func() {
				defer deferutil.Recover()
				ssh.DiscardRequests(requests)
			}()
			go func() {
				defer deferutil.Recover()
				defer func() {
					_ = channel.Close()
				}()
				_, _ = io.Copy(channel, channel)
			}()
		}
	}()

	forwardPayload := ssh.Marshal(&struct {
		Host string
		Port uint32
	}{Host: "myservice", Port: 8000})
	ok, _, err := alice.SendRequest("tcpip-forward", true, forwardPayload)
	if err != nil {
		t.Fatalf("failed to send tcpip-forward: %s", err)
	}
	if !ok {
		t.Fatal("tcpip-forward was rejected")
	}

	// bob reaches alice's service by name through the gateway
	bob := dialTestGateway(t, address, newCertSigner(t, caSigner, "bob", extensions), "bob")
	defer func() {
		_ = bob.Close()
	}()

	tunnel, err := bob.Dial("tcp", "myservice.alice:8000")
	if err != nil {
		t.Fatalf("failed to open tunnel: %s", err)
	}
	defer func() {
		_ = tunnel.Close()
	}()

	message := []byte("hello through the gateway")
	if _, err := tunnel.Write(message); err != nil {
		t.Fatalf("failed to write to tunnel: %s", err)
	}
	// ssh channel conns do not support read deadlines, rely on the test timeout
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(tunnel, echo); err != nil {
		t.Fatalf("failed to read from tunnel: %s", err)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("expected %q, got %q", message, echo)
	}
}

func TestGatewayTunnelRequiresPermission(t *testing.T) {
	t.Parallel()
	address, caSigner, _, _, release := startTestGateway(t)
	defer release()

	// bob has no permit-port-forwarding extension in his certificate
	bob := dialTestGateway(t, address, newCertSigner(t, caSigner, "bob", nil), "bob")
	defer func() {
		_ = bob.Close()
	}()

	if _, err := bob.Dial("tcp", "myservice.alice:8000"); err == nil {
		t.Fatal("expected tunnel to be rejected without permission")
	}
}

func TestGatewayRejectsDisabledUser(t *testing.T) {
	t.Parallel()
	address, caSigner, _, database, release := startTestGateway(t)
	defer release()

	if _, err := database.PutUser(t.Context(), "mallory", func(user *db.User) error {
		user.Disabled = true
		return nil
	}); err != nil {
		t.Fatalf("failed to disable user: %s", err)
	}

	if _, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            "mallory",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(newCertSigner(t, caSigner, "mallory", nil))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}); err == nil {
		t.Fatal("expected authentication to fail for disabled user")
	}
}

func TestGatewayScavengeClosesIdleConnections(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	// any idle time exceeds a zero timeout
	time.Sleep(10 * time.Millisecond)
	instance.ScavengeConnections(time.Nanosecond)

	done := make(chan error, 1)
	go func() {
		done <- client.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("expected idle connection to be closed by scavenger")
	}
}
