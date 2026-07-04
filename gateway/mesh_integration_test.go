package gateway_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/auth"
	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/db/dbtest"
	"github.com/ziyan/gatewaysshd/gateway"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

// newNodeCertSigner returns a signer for a node certificate: signed by the
// peer certificate authority, "peer" principal, node id as key id.
func newNodeCertSigner(t *testing.T, peerCaSigner ssh.Signer, nodeId string) ssh.Signer {
	t.Helper()
	nodeSigner := newTestSigner(t)
	certificate := &ssh.Certificate{
		Key:             nodeSigner.PublicKey(),
		CertType:        ssh.UserCert,
		KeyId:           nodeId,
		ValidPrincipals: []string{auth.PeerUser},
		ValidAfter:      0,
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := certificate.SignCert(rand.Reader, peerCaSigner); err != nil {
		t.Fatalf("failed to sign node certificate: %s", err)
	}
	certSigner, err := ssh.NewCertSigner(certificate, nodeSigner)
	if err != nil {
		t.Fatalf("failed to create node cert signer: %s", err)
	}
	return certSigner
}

// startTestNode starts a gateway participating in the mesh, with its own user
// certificate authority and host key, sharing the given database.
func startTestNode(t *testing.T, database db.Database, userCaSigner, peerCaSigner ssh.Signer, nodeId, postgresAddress string) (string, gateway.Gateway, func()) {
	t.Helper()

	hostSigner := newTestSigner(t)
	sshConfig, err := auth.NewConfig(database, &auth.Settings{
		CaPublicKeys:     []ssh.PublicKey{userCaSigner.PublicKey()},
		PeerCaPublicKeys: []ssh.PublicKey{peerCaSigner.PublicKey()},
		NodeID:           nodeId,
		GeoipDatabase:    "missing-geoip.mmdb",
	})
	if err != nil {
		t.Fatalf("failed to create ssh config: %s", err)
	}
	sshConfig.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	address := listener.Addr().String()

	instance, err := gateway.Open(database, sshConfig, &gateway.Settings{
		Version:               "test",
		NodeID:                nodeId,
		NodeAddress:           address,
		HostPublicKey:         hostSigner.PublicKey(),
		NodeSigner:            newNodeCertSigner(t, peerCaSigner, nodeId),
		PostgresAddress:       postgresAddress,
		PeerDiscoveryInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("failed to open gateway node %s: %s", nodeId, err)
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

	return address, instance, func() {
		if err := listener.Close(); err != nil {
			t.Fatalf("failed to close listener: %s", err)
		}
		instance.Close()
	}
}

// TestGatewayMeshTunnel proves a user on one node can reach a service exposed
// by a user on another node, with each node trusting a different user
// certificate authority and sharing only the database and the peer
// certificate authority.
func TestGatewayMeshTunnel(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSignerA := newTestSigner(t)
	userCaSignerB := newTestSigner(t)

	addressA, _, releaseA := startTestNode(t, database, userCaSignerA, peerCaSigner, "node-a", "")
	defer releaseA()
	addressB, _, releaseB := startTestNode(t, database, userCaSignerB, peerCaSigner, "node-b", "")
	defer releaseB()

	extensions := map[string]string{"permit-port-forwarding": ""}

	// alice connects to node b with a certificate from node b's user ca and
	// advertises an echo service
	alice := dialTestGateway(t, addressB, newCertSigner(t, userCaSignerB, "alice", extensions), "alice")
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
	if err != nil || !ok {
		t.Fatalf("tcpip-forward failed: ok = %v, err = %v", ok, err)
	}

	// bob connects to node a with a certificate from node a's user ca
	bob := dialTestGateway(t, addressA, newCertSigner(t, userCaSignerA, "bob", extensions), "bob")
	defer func() {
		_ = bob.Close()
	}()

	// node a needs its outbound peer connection to node b before it can
	// forward, and a rejected tunnel still dials successfully and surfaces
	// as EOF on first use, so retry the whole echo round trip until
	// discovery has caught up
	message := []byte("hello across the mesh")
	echo := make([]byte, len(message))
	deadline := time.Now().Add(15 * time.Second)
	for {
		echoErr := func() error {
			tunnel, err := bob.Dial("tcp", "myservice.alice:8000")
			if err != nil {
				return err
			}
			defer func() {
				_ = tunnel.Close()
			}()
			if _, err := tunnel.Write(message); err != nil {
				return err
			}
			if _, err := io.ReadFull(tunnel, echo); err != nil {
				return err
			}
			return nil
		}()
		if echoErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed to echo through mesh tunnel: %s", echoErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("expected %q, got %q", message, echo)
	}
}

// TestGatewayPostgresViaPeer proves a node without direct database access can
// reach the central postgres through a peer node's ssh service port.
func TestGatewayPostgresViaPeer(t *testing.T) {
	t.Parallel()
	database, settings, release := dbtest.AcquireDatabaseWithSettings(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)

	hostSigner := newTestSigner(t)
	sshConfig, err := auth.NewConfig(database, &auth.Settings{
		CaPublicKeys:     []ssh.PublicKey{userCaSigner.PublicKey()},
		PeerCaPublicKeys: []ssh.PublicKey{peerCaSigner.PublicKey()},
		NodeID:           "node-central",
		GeoipDatabase:    "missing-geoip.mmdb",
	})
	if err != nil {
		t.Fatalf("failed to create ssh config: %s", err)
	}
	sshConfig.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	defer func() {
		_ = listener.Close()
	}()

	instance, err := gateway.Open(database, sshConfig, &gateway.Settings{
		Version:         "test",
		NodeID:          "node-central",
		NodeAddress:     listener.Addr().String(),
		HostPublicKey:   hostSigner.PublicKey(),
		PostgresAddress: net.JoinHostPort(settings.Host, "5432"),
	})
	if err != nil {
		t.Fatalf("failed to open gateway: %s", err)
	}
	defer instance.Close()

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

	// open the same database through the peer tunnel
	tunneled, err := db.Open(&db.Settings{
		Host:              "postgres",
		Port:              5432,
		User:              settings.User,
		Password:          settings.Password,
		DatabaseName:      settings.DatabaseName,
		PeerAddress:       listener.Addr().String(),
		PeerSigner:        newNodeCertSigner(t, peerCaSigner, "node-remote"),
		PeerHostPublicKey: hostSigner.PublicKey(),
	})
	if err != nil {
		t.Fatalf("failed to open database via peer: %s", err)
	}
	defer func() {
		_ = tunneled.Close()
	}()

	if _, err := tunneled.PutUser(t.Context(), "carol", func(user *db.User) error {
		user.Comment = "created through the peer tunnel"
		return nil
	}); err != nil {
		t.Fatalf("failed to put user via peer tunnel: %s", err)
	}

	// visible through the direct connection
	user, err := database.GetUser(t.Context(), "carol")
	if err != nil {
		t.Fatalf("failed to get user: %s", err)
	}
	if user == nil || user.Comment != "created through the peer tunnel" {
		t.Fatalf("unexpected user: %+v", user)
	}
}

// TestGatewayPeerAuthUsesSeparateCa proves the user certificate authority
// cannot authenticate peers and the peer certificate authority cannot
// authenticate users.
func TestGatewayPeerAuthUsesSeparateCa(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)

	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")
	defer releaseNode()

	// a peer certificate signed by the user ca must be rejected
	if _, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            auth.PeerUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(newNodeCertSigner(t, userCaSigner, "node-x"))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}); err == nil {
		t.Fatal("expected peer authentication with user ca to fail")
	}

	// a user certificate signed by the peer ca must be rejected
	if _, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            "mallory",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(newCertSigner(t, peerCaSigner, "mallory", nil))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}); err == nil {
		t.Fatal("expected user authentication with peer ca to fail")
	}
}

// TestGatewayPostgresRequiresPeer proves regular users cannot reach the
// postgres bridge even with port forwarding permission.
func TestGatewayPostgresRequiresPeer(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "127.0.0.1:5432")
	defer releaseNode()

	extensions := map[string]string{"permit-port-forwarding": ""}
	client := dialTestGateway(t, address, newCertSigner(t, userCaSigner, "alice", extensions), "alice")
	defer func() {
		_ = client.Close()
	}()

	if _, err := client.Dial("tcp", "postgres:5432"); err == nil {
		t.Fatal("expected postgres tunnel to be rejected for regular users")
	}
}

// TestGatewayMeshUnknownUserFails proves tunnels to users unknown to the mesh
// are cleanly rejected.
func TestGatewayMeshUnknownUserFails(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")
	defer releaseNode()

	extensions := map[string]string{"permit-port-forwarding": ""}
	client := dialTestGateway(t, address, newCertSigner(t, userCaSigner, "bob", extensions), "bob")
	defer func() {
		_ = client.Close()
	}()

	// a rejected tunnel is accepted then immediately closed by the gateway,
	// so the failure surfaces as EOF on first read
	tunnel, err := client.Dial("tcp", "nothing.nobody:1234")
	if err != nil {
		return // rejected outright is also fine
	}
	defer func() {
		_ = tunnel.Close()
	}()
	buffer := make([]byte, 1)
	if _, err := tunnel.Read(buffer); err == nil {
		t.Fatal("expected tunnel to unknown user to fail")
	}
}

// TestGatewayNodeRegistration proves nodes register themselves online in the
// database and mark themselves offline on close.
func TestGatewayNodeRegistration(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")

	node, err := database.GetNode(t.Context(), "node-a")
	if err != nil {
		t.Fatalf("failed to get node: %s", err)
	}
	if node == nil || !node.Online || node.Address != address || node.HostPublicKey == "" {
		t.Fatalf("unexpected node registration: %+v", node)
	}

	releaseNode()

	node, err = database.GetNode(t.Context(), "node-a")
	if err != nil {
		t.Fatalf("failed to get node: %s", err)
	}
	if node == nil || node.Online {
		t.Fatalf("expected node to be offline after close, got %+v", node)
	}
}
