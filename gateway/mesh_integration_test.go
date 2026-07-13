package gateway_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync/atomic"
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
	sshConfig, revokeLoginFlags, err := auth.NewConfig(database, &auth.Settings{
		CAPublicKeys:     []ssh.PublicKey{userCaSigner.PublicKey()},
		PeerCAPublicKeys: []ssh.PublicKey{peerCaSigner.PublicKey()},
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
		RevokeLoginFlags:      revokeLoginFlags,
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

	// a second tunnel rides the cached route, no database lookup involved
	tunnel, err := bob.Dial("tcp", "myservice.alice:8000")
	if err != nil {
		t.Fatalf("failed to open second tunnel via cached route: %s", err)
	}
	if _, err := tunnel.Write(message); err != nil {
		t.Fatalf("failed to write via cached route: %s", err)
	}
	if _, err := io.ReadFull(tunnel, echo); err != nil {
		t.Fatalf("failed to echo via cached route: %s", err)
	}
	_ = tunnel.Close()
	if !bytes.Equal(echo, message) {
		t.Fatalf("expected %q via cached route, got %q", message, echo)
	}

	// a tunnel the remote node rejects invalidates the cached route (it dials
	// successfully and surfaces as EOF on first use); the next open re-reads
	// the database and succeeds again
	rejected, err := bob.Dial("tcp", "nosuchservice.alice:9999")
	if err == nil {
		if _, err := rejected.Read(make([]byte, 1)); err == nil {
			t.Fatal("expected tunnel to unadvertised service to fail")
		}
		_ = rejected.Close()
	}
	tunnel, err = bob.Dial("tcp", "myservice.alice:8000")
	if err != nil {
		t.Fatalf("failed to reopen tunnel after invalidation: %s", err)
	}
	_ = tunnel.Close()
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
	sshConfig, _, err := auth.NewConfig(database, &auth.Settings{
		CAPublicKeys:     []ssh.PublicKey{userCaSigner.PublicKey()},
		PeerCAPublicKeys: []ssh.PublicKey{peerCaSigner.PublicKey()},
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

	// a regular user gets no postgres bridge: "postgres:5432" is just an
	// ordinary (unregistered) service, so the tunnel is accepted then closed
	// rather than bridged to the database
	tunnel, err := client.Dial("tcp", "postgres:5432")
	if err != nil {
		return // rejected outright is also acceptable
	}
	defer func() {
		_ = tunnel.Close()
	}()
	if _, err := tunnel.Read(make([]byte, 1)); err == nil {
		t.Fatal("regular user reached the postgres bridge")
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
	if time.Since(node.OnlineAt) > time.Minute {
		t.Fatalf("expected a fresh online heartbeat, got %s", node.OnlineAt)
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

// TestGatewayDiscoverySkipsCrashedNodes proves a node whose row still says
// online but whose heartbeat went stale is not dialed: a crashed node cannot
// mark itself offline, so peers must age it out instead of redialing forever.
func TestGatewayDiscoverySkipsCrashedNodes(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	// a listener that counts connection attempts and closes them right away
	countingListener := func() (string, *atomic.Int32, func()) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to listen: %s", err)
		}
		var count atomic.Int32
		go func() {
			defer deferutil.Recover()
			for {
				socket, err := listener.Accept()
				if err != nil {
					return
				}
				count.Add(1)
				_ = socket.Close()
			}
		}()
		return listener.Addr().String(), &count, func() { _ = listener.Close() }
	}
	freshAddress, freshCount, closeFresh := countingListener()
	defer closeFresh()
	crashedAddress, crashedCount, closeCrashed := countingListener()
	defer closeCrashed()

	// seed one node with a fresh heartbeat and one whose heartbeat went stale
	seedNode := func(nodeId, address string, onlineAt time.Time) {
		hostPublicKey := string(ssh.MarshalAuthorizedKey(newTestSigner(t).PublicKey()))
		if _, err := database.PutNode(t.Context(), nodeId, func(node *db.Node) error {
			node.Address = address
			node.HostPublicKey = hostPublicKey
			node.Online = true
			node.OnlineAt = onlineAt
			return nil
		}); err != nil {
			t.Fatalf("failed to seed node %s: %s", nodeId, err)
		}
	}
	seedNode("node-fresh", freshAddress, time.Now())
	seedNode("node-crashed", crashedAddress, time.Now().Add(-5*time.Minute))

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	_, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")
	defer releaseNode()

	// wait until the fresh node has been dialed a few times, proving several
	// discovery cycles have processed both seeded rows
	deadline := time.Now().Add(10 * time.Second)
	for freshCount.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("fresh node was only dialed %d times", freshCount.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if count := crashedCount.Load(); count != 0 {
		t.Fatalf("crashed node was dialed %d times", count)
	}
}

// TestGatewayPeerExtensionNotForgeable proves a regular user certificate that
// carries a "peer"/"identity" extension is NOT treated as a peer node: the
// user certificate authority must not be able to grant node capabilities.
func TestGatewayPeerExtensionNotForgeable(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	// this node has a real postgres address, so a genuine peer could bridge it
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "127.0.0.1:5432")
	defer releaseNode()

	// a user certificate (signed by the user CA) forging the peer markers, and
	// deliberately without permit-port-forwarding
	forged := newCertSigner(t, userCaSigner, "mallory", map[string]string{
		"peer":     "",
		"identity": "node-evil",
	})
	client := dialTestGateway(t, address, forged, "mallory")
	defer func() {
		_ = client.Close()
	}()

	// if the forged extension had been honored, nodeId != "" would bypass the
	// port-forward check and unlock the postgres bridge; both must be denied
	if _, err := client.Dial("tcp", "postgres:5432"); err == nil {
		t.Fatal("forged peer extension granted postgres access")
	}
	if _, err := client.Dial("tcp", "anything.someone:22"); err == nil {
		t.Fatal("forged peer extension bypassed port-forward permission")
	}
}

// TestGatewayPeerSessionRejected proves a peer connection cannot open a session
// channel (which would drive PutUser with an empty username).
func TestGatewayPeerSessionRejected(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")
	defer releaseNode()

	// dial as a genuine peer node
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            auth.PeerUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(newNodeCertSigner(t, peerCaSigner, "node-b"))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to dial as peer: %s", err)
	}
	defer func() {
		_ = client.Close()
	}()

	// the session channel is rejected via accept-then-close, so NewSession may
	// succeed client-side but running a command (which would reach the session
	// handlers) must fail and must not create an empty-named user
	session, err := client.NewSession()
	if err == nil {
		// a bare JSON command takes the legacy status path -> reportStatus ->
		// PutUser(self.user); for a peer self.user is "", so without the
		// session gate this would create an empty-named user
		_ = session.Run("{}")
		_ = session.Close()
	}

	user, err := database.GetUser(t.Context(), "")
	if err != nil {
		t.Fatalf("failed to query empty user: %s", err)
	}
	if user != nil {
		t.Fatalf("peer session created an empty-named user: %+v", user)
	}
}
