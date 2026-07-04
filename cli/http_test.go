package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeGateway implements gateway.Gateway for handler tests
type fakeGateway struct {
	users       map[string]interface{}
	screenshots map[string][]byte
}

func (self *fakeGateway) Close()                            {}
func (self *fakeGateway) HandleConnection(net.Conn)         {}
func (self *fakeGateway) HandleSocksConnection(net.Conn)    {}
func (self *fakeGateway) HTTPProxyHandler() http.Handler    { return nil }
func (self *fakeGateway) ScavengeConnections(time.Duration) {}
func (self *fakeGateway) ListUsers(context.Context) (interface{}, error) {
	return map[string]interface{}{"users": []string{"alice"}}, nil
}

func (self *fakeGateway) GetUser(ctx context.Context, userId string) (interface{}, error) {
	user, ok := self.users[userId]
	if !ok {
		return nil, nil
	}
	return user, nil
}

func (self *fakeGateway) GetUserScreenshot(ctx context.Context, userId string) ([]byte, error) {
	return self.screenshots[userId], nil
}

func newTestServer() *httptest.Server {
	return httptest.NewServer(newHttpHandler(&fakeGateway{
		users: map[string]interface{}{
			"alice": map[string]interface{}{"id": "alice"},
		},
		screenshots: map[string][]byte{
			"alice": {0x89, 0x50, 0x4e, 0x47},
		},
	}))
}

func getResponse(t *testing.T, url string) (*http.Response, func()) {
	t.Helper()
	response, err := http.Get(url) //nolint:gosec
	if err != nil {
		t.Fatalf("failed to get %s: %s", url, err)
	}
	return response, func() {
		if err := response.Body.Close(); err != nil {
			t.Fatalf("failed to close response body: %s", err)
		}
	}
}

func TestHttpListUsers(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	response, closeBody := getResponse(t, server.URL+"/api/user")
	defer closeBody()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type %q", contentType)
	}
	var body map[string][]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}
	if len(body["users"]) != 1 || body["users"][0] != "alice" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestHttpGetUser(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	response, closeBody := getResponse(t, server.URL+"/api/user/alice")
	defer closeBody()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}
	if body["id"] != "alice" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestHttpGetUserNotFound(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	response, closeBody := getResponse(t, server.URL+"/api/user/missing")
	defer closeBody()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.StatusCode)
	}
}

func TestHttpGetUserScreenshot(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	response, closeBody := getResponse(t, server.URL+"/api/user/alice/screenshot")
	defer closeBody()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("unexpected content type %q", contentType)
	}
}

func TestHttpGetUserScreenshotNotFound(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	response, closeBody := getResponse(t, server.URL+"/api/user/missing/screenshot")
	defer closeBody()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.StatusCode)
	}
}
