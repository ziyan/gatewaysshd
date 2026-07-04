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
)

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

	response, err := instance.GetUser("alice")
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

	response, err := instance.GetUser("alice")
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

	saved, err := instance.GetUserScreenshot("alice")
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
	if _, err := database.PutUser("root", func(user *db.User) error {
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
