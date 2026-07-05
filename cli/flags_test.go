package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/crypto/ssh"
)

// runWithFlags runs body with a cli command parsed from the given arguments,
// the way the real app invokes the parse helpers.
func runWithFlags(t *testing.T, arguments []string, body func(command *cli.Command) error) error {
	t.Helper()
	var bodyError error
	command := &cli.Command{
		Name:  "gatewaysshd",
		Flags: flags,
		Action: func(ctx context.Context, command *cli.Command) error {
			bodyError = body(command)
			return nil
		},
	}
	if err := command.Run(context.Background(), append([]string{"gatewaysshd"}, arguments...)); err != nil {
		t.Fatalf("failed to run command: %s", err)
	}
	return bodyError
}

func newTestKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %s", err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("failed to convert public key: %s", err)
	}
	return privateKey, sshPublicKey
}

func writeTestFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("failed to write %s: %s", name, err)
	}
	return path
}

func TestParseCaPublicKeysReadsMultipleKeys(t *testing.T) {
	_, firstPublicKey := newTestKey(t)
	_, secondPublicKey := newTestKey(t)
	content := append(ssh.MarshalAuthorizedKey(firstPublicKey), ssh.MarshalAuthorizedKey(secondPublicKey)...)
	path := writeTestFile(t, "ca.pub", content)

	if err := runWithFlags(t, []string{"--ca-public-key", path}, func(command *cli.Command) error {
		caPublicKeys, err := parseCaPublicKeys(command)
		if err != nil {
			return err
		}
		if len(caPublicKeys) != 2 {
			t.Fatalf("expected 2 keys, got %d", len(caPublicKeys))
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to parse ca public keys: %s", err)
	}
}

func TestParseCaPublicKeysMissingFile(t *testing.T) {
	if err := runWithFlags(t, []string{"--ca-public-key", "/nonexistent/ca.pub"}, func(command *cli.Command) error {
		_, err := parseCaPublicKeys(command)
		return err
	}); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseHostSignerPlainKey(t *testing.T) {
	privateKey, publicKey := newTestKey(t)
	pemBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatalf("failed to marshal private key: %s", err)
	}
	privatePath := writeTestFile(t, "id_ed25519", pem.EncodeToMemory(pemBlock))
	publicPath := writeTestFile(t, "id_ed25519.pub", ssh.MarshalAuthorizedKey(publicKey))

	if err := runWithFlags(t, []string{"--host-private-key", privatePath, "--host-public-key", publicPath}, func(command *cli.Command) error {
		hostSigner, err := parseHostSigner(command)
		if err != nil {
			return err
		}
		if hostSigner.PublicKey().Type() != ssh.KeyAlgoED25519 {
			t.Fatalf("unexpected key type %s", hostSigner.PublicKey().Type())
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to parse host signer: %s", err)
	}
}

func TestParseHostSignerCertificate(t *testing.T) {
	caPrivateKey, _ := newTestKey(t)
	caSigner, err := ssh.NewSignerFromKey(caPrivateKey)
	if err != nil {
		t.Fatalf("failed to create ca signer: %s", err)
	}

	hostPrivateKey, hostPublicKey := newTestKey(t)
	certificate := &ssh.Certificate{
		Key:             hostPublicKey,
		CertType:        ssh.HostCert,
		KeyId:           "gateway",
		ValidPrincipals: []string{"gateway.example.com"},
		ValidAfter:      0,
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("failed to sign certificate: %s", err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(hostPrivateKey, "")
	if err != nil {
		t.Fatalf("failed to marshal private key: %s", err)
	}
	privatePath := writeTestFile(t, "id_ed25519", pem.EncodeToMemory(pemBlock))
	certificatePath := writeTestFile(t, "id_ed25519-cert.pub", ssh.MarshalAuthorizedKey(certificate))

	if err := runWithFlags(t, []string{"--host-private-key", privatePath, "--host-public-key", certificatePath}, func(command *cli.Command) error {
		hostSigner, err := parseHostSigner(command)
		if err != nil {
			return err
		}
		if _, ok := hostSigner.PublicKey().(*ssh.Certificate); !ok {
			t.Fatalf("expected certificate signer, got %s", hostSigner.PublicKey().Type())
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to parse host signer: %s", err)
	}
}

func TestParseIdleTimeout(t *testing.T) {
	if err := runWithFlags(t, []string{"--idle-timeout", "90s"}, func(command *cli.Command) error {
		idleTimeout, err := parseIdleTimeout(command)
		if err != nil {
			return err
		}
		if idleTimeout != 90*time.Second {
			t.Fatalf("expected 90s, got %s", idleTimeout)
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to parse idle timeout: %s", err)
	}

	if err := runWithFlags(t, []string{"--idle-timeout", "bogus"}, func(command *cli.Command) error {
		_, err := parseIdleTimeout(command)
		return err
	}); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestParsePostgresPasswordInline(t *testing.T) {
	if err := runWithFlags(t, []string{"--postgres-password", "hunter2"}, func(command *cli.Command) error {
		password, err := parsePostgresPassword(command)
		if err != nil {
			return err
		}
		if password != "hunter2" {
			t.Fatalf("expected inline password, got %q", password)
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to parse postgres password: %s", err)
	}
}

func TestParsePostgresPasswordFileTakesPrecedence(t *testing.T) {
	path := writeTestFile(t, "password", []byte("from-file\n"))

	if err := runWithFlags(t, []string{"--postgres-password", "inline", "--postgres-password-file", path}, func(command *cli.Command) error {
		password, err := parsePostgresPassword(command)
		if err != nil {
			return err
		}
		if password != "from-file" {
			t.Fatalf("expected password from file with trailing newline trimmed, got %q", password)
		}
		return nil
	}); err != nil {
		t.Fatalf("failed to parse postgres password: %s", err)
	}
}

func TestParsePostgresPasswordFileMissing(t *testing.T) {
	if err := runWithFlags(t, []string{"--postgres-password-file", "/nonexistent/password"}, func(command *cli.Command) error {
		_, err := parsePostgresPassword(command)
		return err
	}); err == nil {
		t.Fatal("expected error for missing password file")
	}
}
