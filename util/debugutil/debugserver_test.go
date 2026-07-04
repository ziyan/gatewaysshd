package debugutil

import (
	"fmt"
	"net"
	"net/http"
	"testing"
)

func TestRunDebugServerServesPprof(t *testing.T) {
	// reserve a free port, then hand it to the debug server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	endpoint := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to close listener: %s", err)
	}

	stopDebugServer, err := RunDebugServer(endpoint)
	if err != nil {
		t.Fatalf("failed to run debug server: %s", err)
	}
	defer stopDebugServer()

	response, err := http.Get(fmt.Sprintf("http://%s/debug/pprof/cmdline", endpoint))
	if err != nil {
		t.Fatalf("failed to get pprof endpoint: %s", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Fatalf("failed to close response body: %s", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
}

func TestRunDebugServerInvalidEndpoint(t *testing.T) {
	if _, err := RunDebugServer("256.256.256.256:99999"); err == nil {
		t.Fatal("expected error for invalid endpoint")
	}
}
