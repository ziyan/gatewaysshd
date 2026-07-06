package gateway_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db"
	"github.com/ziyan/gatewaysshd/db/dbtest"
)

// userList is the shape of listUsers / listOnlineUsers output for assertions.
type userList struct {
	Users []struct {
		ID     string `json:"id"`
		Online bool   `json:"online"`
		NodeID string `json:"nodeId"`
	} `json:"users"`
	Meta struct {
		TotalCount int `json:"totalCount"`
	} `json:"meta"`
}

// parseUserList runs a user listing command and parses its JSON output.
func parseUserList(t *testing.T, client *ssh.Client, command string) userList {
	t.Helper()
	var result userList
	if err := json.Unmarshal([]byte(runCommand(t, client, command)), &result); err != nil {
		t.Fatalf("failed to parse %s output: %s", command, err)
	}
	return result
}

func runCommand(t *testing.T, client *ssh.Client, command string) string {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	defer func() {
		_ = session.Close()
	}()
	output, err := session.Output(command)
	if err != nil {
		t.Fatalf("failed to run %q: %s", command, err)
	}
	return string(output)
}

// runCommandWithInput runs an exec command feeding data to its stdin, the way
// clients push reportStatus / reportScreenshot payloads.
func runCommandWithInput(t *testing.T, client *ssh.Client, command string, input []byte) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	defer func() {
		_ = session.Close()
	}()
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("failed to open stdin: %s", err)
	}
	if err := session.Start(command); err != nil {
		t.Fatalf("failed to start %q: %s", command, err)
	}
	if _, err := stdin.Write(input); err != nil {
		t.Fatalf("failed to write input: %s", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("failed to close stdin: %s", err)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("command %q failed: %s", command, err)
	}
}

func TestGatewayVersionAndHelpCommands(t *testing.T) {
	t.Parallel()
	address, caSigner, _, _, release := startTestGateway(t)
	defer release()

	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	if output := runCommand(t, client, "version"); strings.TrimSpace(output) != "test" {
		t.Fatalf("unexpected version output: %q", output)
	}
	if output := runCommand(t, client, "help"); !strings.Contains(output, "ping server") {
		t.Fatalf("unexpected help output: %q", output)
	}
}

func TestGatewayInvalidCommandFails(t *testing.T) {
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
	if _, err := session.Output("bogus"); err == nil {
		t.Fatal("expected invalid command to fail")
	}
}

func TestGatewayReportStatus(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	// reportStatus expects a gzip compressed json payload on stdin
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(`{"healthy":true}`)); err != nil {
		t.Fatalf("failed to compress payload: %s", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %s", err)
	}
	runCommandWithInput(t, client, "reportStatus", compressed.Bytes())

	response, err := instance.GetUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("failed to get user: %s", err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("failed to marshal user: %s", err)
	}
	if !strings.Contains(string(raw), `"healthy":true`) {
		t.Fatalf("expected reported status in user, got %s", raw)
	}
}

func TestGatewayLegacyJsonStatusReport(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	// legacy clients send the json status as the exec command itself
	_ = runCommand(t, client, `{"legacy":true}`)

	response, err := instance.GetUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("failed to get user: %s", err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("failed to marshal user: %s", err)
	}
	if !strings.Contains(string(raw), `"legacy":true`) {
		t.Fatalf("expected legacy status in user, got %s", raw)
	}
}

func TestGatewayReportScreenshot(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	client := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = client.Close()
	}()

	screenshot := []byte{0x89, 0x50, 0x4e, 0x47}
	runCommandWithInput(t, client, "reportScreenshot", screenshot)

	saved, err := instance.GetUserScreenshot(t.Context(), "alice")
	if err != nil {
		t.Fatalf("failed to get screenshot: %s", err)
	}
	if !bytes.Equal(saved, screenshot) {
		t.Fatalf("expected %v, got %v", screenshot, saved)
	}
}

func TestGatewayStatusAndUserCommands(t *testing.T) {
	t.Parallel()
	address, caSigner, _, _, release := startTestGateway(t)
	defer release()

	// status, listUsers and getUser require port forwarding permission
	plain := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", nil), "alice")
	defer func() {
		_ = plain.Close()
	}()
	for _, command := range []string{"status", "listUsers"} {
		session, err := plain.NewSession()
		if err != nil {
			t.Fatalf("failed to create session: %s", err)
		}
		if _, err := session.Output(command); err == nil {
			t.Fatalf("expected %s to fail without permission", command)
		}
		_ = session.Close()
	}

	extensions := map[string]string{"permit-port-forwarding": ""}
	forwarding := dialTestGateway(t, address, newCertSigner(t, caSigner, "bob", extensions), "bob")
	defer func() {
		_ = forwarding.Close()
	}()

	if output := runCommand(t, forwarding, "status"); !strings.Contains(output, `"connections"`) {
		t.Fatalf("unexpected gateway status output: %q", output)
	}
	if output := runCommand(t, forwarding, "listUsers"); !strings.Contains(output, `"totalCount": 2`) {
		t.Fatalf("unexpected listUsers output: %q", output)
	}
	if output := runCommand(t, forwarding, "getUser alice"); !strings.Contains(output, `"id": "alice"`) {
		t.Fatalf("unexpected getUser output: %q", output)
	}
}

func TestGatewayKickUserRequiresAdministrator(t *testing.T) {
	t.Parallel()
	address, caSigner, _, database, release := startTestGateway(t)
	defer release()

	extensions := map[string]string{"permit-port-forwarding": ""}

	victim := dialTestGateway(t, address, newCertSigner(t, caSigner, "victim", extensions), "victim")
	defer func() {
		_ = victim.Close()
	}()

	// a non-administrator cannot kick
	bob := dialTestGateway(t, address, newCertSigner(t, caSigner, "bob", extensions), "bob")
	defer func() {
		_ = bob.Close()
	}()
	session, err := bob.NewSession()
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	if _, err := session.Output("kickUser victim"); err == nil {
		t.Fatal("expected kickUser to fail without administrator")
	}
	_ = session.Close()

	// the administrator flag is granted at auth time from the user record
	if _, err := database.PutUser(t.Context(), "root", func(user *db.User) error {
		user.Administrator = true
		return nil
	}); err != nil {
		t.Fatalf("failed to mark root as administrator: %s", err)
	}

	root := dialTestGateway(t, address, newCertSigner(t, caSigner, "root", extensions), "root")
	defer func() {
		_ = root.Close()
	}()
	_ = runCommand(t, root, "kickUser victim")

	// the victim's connection must be closed by the gateway
	done := make(chan error, 1)
	go func() {
		done <- victim.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("expected victim connection to be closed")
	}
}

// TestGatewayListOnlineUsers proves listOnlineUsers returns only currently
// connected users and that each user record carries its nodeId.
func TestGatewayListOnlineUsers(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")
	defer releaseNode()

	// a user that exists in the database but is not connected
	if _, err := database.PutUser(t.Context(), "ghost", func(user *db.User) error {
		return nil
	}); err != nil {
		t.Fatalf("failed to create offline user: %s", err)
	}

	// bob connects (online) and can run the listing commands
	extensions := map[string]string{"permit-port-forwarding": ""}
	bob := dialTestGateway(t, address, newCertSigner(t, userCaSigner, "bob", extensions), "bob")
	defer func() {
		_ = bob.Close()
	}()

	// listUsers includes both the online (bob) and offline (ghost) users
	all := parseUserList(t, bob, "listUsers")
	if all.Meta.TotalCount != 2 {
		t.Fatalf("expected listUsers totalCount 2, got %d", all.Meta.TotalCount)
	}

	// listOnlineUsers returns only bob, with online=true and nodeId set
	online := parseUserList(t, bob, "listOnlineUsers")
	if online.Meta.TotalCount != 1 {
		t.Fatalf("expected listOnlineUsers totalCount 1, got %d", online.Meta.TotalCount)
	}
	user := online.Users[0]
	if user.ID != "bob" || !user.Online {
		t.Fatalf("unexpected online user: %+v", user)
	}
	if user.NodeID != "node-a" {
		t.Fatalf("expected nodeId node-a, got %q", user.NodeID)
	}
	for _, candidate := range online.Users {
		if candidate.ID == "ghost" {
			t.Fatal("listOnlineUsers included an offline user")
		}
	}
}

// TestGatewayListOnlineUsersMeshWide proves online status is mesh-wide: a user
// connected only to another node still shows up as online, with that node's id.
func TestGatewayListOnlineUsersMeshWide(t *testing.T) {
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

	// carol connects only to node b
	carol := dialTestGateway(t, addressB, newCertSigner(t, userCaSignerB, "carol", extensions), "carol")
	defer func() {
		_ = carol.Close()
	}()

	// bob connects to node a and queries from there
	bob := dialTestGateway(t, addressA, newCertSigner(t, userCaSignerA, "bob", extensions), "bob")
	defer func() {
		_ = bob.Close()
	}()

	online := parseUserList(t, bob, "listOnlineUsers")
	nodes := make(map[string]string)
	for _, user := range online.Users {
		nodes[user.ID] = user.NodeID
	}
	if node, ok := nodes["carol"]; !ok || node != "node-b" {
		t.Fatalf("expected carol online on node-b, got %+v", nodes)
	}
	if node, ok := nodes["bob"]; !ok || node != "node-a" {
		t.Fatalf("expected bob online on node-a, got %+v", nodes)
	}
}

// TestGatewayReleasesUserNodeOnDisconnect proves the user's node_id is
// released when their last connection to the node closes, so a node they are
// still connected to can adopt them on its next heartbeat.
func TestGatewayReleasesUserNodeOnDisconnect(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSigner := newTestSigner(t)
	address, _, releaseNode := startTestNode(t, database, userCaSigner, peerCaSigner, "node-a", "")
	defer releaseNode()

	extensions := map[string]string{"permit-port-forwarding": ""}
	bob := dialTestGateway(t, address, newCertSigner(t, userCaSigner, "bob", extensions), "bob")
	defer func() {
		_ = bob.Close()
	}()

	user, err := database.GetUser(t.Context(), "bob")
	if err != nil || user == nil || user.NodeID != "node-a" {
		t.Fatalf("expected bob on node-a, got %+v err %v", user, err)
	}

	if err := bob.Close(); err != nil {
		t.Fatalf("failed to close connection: %s", err)
	}

	// the connection teardown that releases node_id is asynchronous
	deadline := time.Now().Add(10 * time.Second)
	for {
		user, err := database.GetUser(t.Context(), "bob")
		if err != nil {
			t.Fatalf("failed to get user: %s", err)
		}
		if user.NodeID == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected node_id to be released, still %q", user.NodeID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
