package gateway

import (
	"bytes"
	"net"
	"reflect"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSplitCommand(t *testing.T) {
	testCases := []struct {
		command string
		want    []string
	}{
		{"status", []string{"status"}},
		{"getUser alice", []string{"getUser", "alice"}},
		{`kickUser "alice bob"`, []string{"kickUser", "alice bob"}},
		{"", nil},
	}
	for _, testCase := range testCases {
		got := splitCommand(testCase.command)
		if !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("splitCommand(%q) = %v, want %v", testCase.command, got, testCase.want)
		}
	}
}

func TestForwardRequestRoundTrip(t *testing.T) {
	payload := ssh.Marshal(&forwardRequest{Host: "myservice", Port: 8000})
	request, err := unmarshalForwardRequest(payload)
	if err != nil {
		t.Fatalf("failed to unmarshal forward request: %s", err)
	}
	if request.Host != "myservice" || request.Port != 8000 {
		t.Fatalf("unexpected request: %+v", request)
	}
}

func TestForwardRequestRejectsInvalidPort(t *testing.T) {
	payload := ssh.Marshal(&forwardRequest{Host: "myservice", Port: 65536})
	if _, err := unmarshalForwardRequest(payload); err != ErrInvalidForwardRequest {
		t.Fatalf("expected %s, got %v", ErrInvalidForwardRequest, err)
	}
}

func TestForwardRequestRejectsGarbage(t *testing.T) {
	if _, err := unmarshalForwardRequest([]byte{0x01}); err == nil {
		t.Fatal("expected error for garbage payload")
	}
}

func TestTunnelDataRoundTrip(t *testing.T) {
	original := &tunnelData{
		Host:          "myservice",
		Port:          8000,
		OriginAddress: "127.0.0.1",
		OriginPort:    54321,
	}
	data, err := unmarshalTunnelData(marshalTunnelData(original))
	if err != nil {
		t.Fatalf("failed to unmarshal tunnel data: %s", err)
	}
	if *data != *original {
		t.Fatalf("expected %+v, got %+v", original, data)
	}
}

func TestTunnelDataRejectsInvalidPorts(t *testing.T) {
	for _, original := range []*tunnelData{
		{Host: "myservice", Port: 65536},
		{Host: "myservice", Port: 8000, OriginPort: 65536},
	} {
		if _, err := unmarshalTunnelData(marshalTunnelData(original)); err != ErrInvalidTunnelData {
			t.Fatalf("expected %s for %+v, got %v", ErrInvalidTunnelData, original, err)
		}
	}
}

func TestUsageStatsUpdate(t *testing.T) {
	usage := newUsage()
	before := usage.usedAt

	usage.read(100)
	usage.write(25)
	usage.update(3, 4)

	if usage.bytesRead != 103 {
		t.Fatalf("expected 103 bytes read, got %d", usage.bytesRead)
	}
	if usage.bytesWritten != 29 {
		t.Fatalf("expected 29 bytes written, got %d", usage.bytesWritten)
	}
	if usage.usedAt.Before(before) {
		t.Fatalf("expected usedAt to advance, got %s < %s", usage.usedAt, before)
	}
}

func TestWrappedConnTracksUsage(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer func() {
		_ = clientSide.Close()
	}()

	usage := newUsage()
	wrapped := wrapConn(serverSide, usage)
	defer func() {
		_ = wrapped.Close()
	}()

	message := []byte("hello")
	go func() {
		_, _ = clientSide.Write(message)
	}()

	buffer := make([]byte, len(message))
	if err := wrapped.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %s", err)
	}
	if _, err := wrapped.Read(buffer); err != nil {
		t.Fatalf("failed to read: %s", err)
	}
	if !bytes.Equal(buffer, message) {
		t.Fatalf("expected %q, got %q", message, buffer)
	}

	go func() {
		discard := make([]byte, len(message))
		_, _ = clientSide.Read(discard)
	}()
	if _, err := wrapped.Write(message); err != nil {
		t.Fatalf("failed to write: %s", err)
	}

	if usage.bytesRead != uint64(len(message)) {
		t.Fatalf("expected %d bytes read, got %d", len(message), usage.bytesRead)
	}
	if usage.bytesWritten != uint64(len(message)) {
		t.Fatalf("expected %d bytes written, got %d", len(message), usage.bytesWritten)
	}
}
