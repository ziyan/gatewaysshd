package gateway_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/db/dbtest"
	"github.com/ziyan/gatewaysshd/gateway"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

// exposeEchoService connects the given client to the gateway and advertises an
// echo service on host:port, so proxy tests have something to reach.
func exposeEchoService(t *testing.T, client *ssh.Client, host string, port uint32) {
	t.Helper()
	serveForwarded(t, client, host, port, func(channel ssh.Channel) {
		_, _ = io.Copy(channel, channel)
	})
}

// exposeHttpService advertises an http service on host:port that answers every
// request with a canned 200 response carrying body.
func exposeHttpService(t *testing.T, client *ssh.Client, host string, port uint32, body string) {
	t.Helper()
	serveForwarded(t, client, host, port, func(channel ssh.Channel) {
		reader := bufio.NewReader(channel)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" || line == "\n" {
				break
			}
		}
		_, _ = fmt.Fprintf(channel, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	})
}

// serveForwarded advertises host:port and runs handle on each accepted
// forwarded-tcpip channel.
func serveForwarded(t *testing.T, client *ssh.Client, host string, port uint32, handle func(ssh.Channel)) {
	t.Helper()
	forwarded := client.HandleChannelOpen("forwarded-tcpip")
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
				handle(channel)
			}()
		}
	}()

	payload := ssh.Marshal(&struct {
		Host string
		Port uint32
	}{Host: host, Port: port})
	ok, _, err := client.SendRequest("tcpip-forward", true, payload)
	if err != nil || !ok {
		t.Fatalf("tcpip-forward failed: ok = %v, err = %v", ok, err)
	}
}

// startSocksProxy starts a socks5 listener served by the gateway and returns
// its address and a cleanup func.
func startSocksProxy(t *testing.T, instance gateway.Gateway) (string, func()) {
	t.Helper()
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
				instance.HandleSocksConnection(socket)
			}()
		}
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
	}
}

// startHttpProxy starts an http forward-proxy listener served by the gateway.
func startHttpProxy(t *testing.T, instance gateway.Gateway) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	server := &http.Server{Handler: instance.HTTPProxyHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer deferutil.Recover()
		_ = server.Serve(listener)
	}()
	return listener.Addr().String(), func() {
		_ = server.Close()
	}
}

// socksConnect performs a socks5 CONNECT handshake to host:port through the
// proxy and returns the established connection, or an error if the proxy
// refuses (including a non-success reply code).
func socksConnect(proxyAddress, host string, port uint16) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddress, 10*time.Second)
	if err != nil {
		return nil, err
	}

	// method negotiation: version 5, one method, no-auth
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks method rejected: %v", reply)
	}

	// CONNECT request with a domain-name address
	hostLength := byte(len(host)) // #nosec G115 - test host is short
	request := []byte{0x05, 0x01, 0x00, 0x03, hostLength}
	request = append(request, []byte(host)...)
	request = binary.BigEndian.AppendUint16(request, port)
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// reply: version, rep, rsv, atyp(ipv4), 4-byte address, 2-byte port
	response := make([]byte, 10)
	if _, err := io.ReadFull(conn, response); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks connect failed with reply code %d", response[1])
	}
	return conn, nil
}

// socksRoundTrip connects through the socks proxy and echoes message.
func socksRoundTrip(proxyAddress, host string, port uint16, message []byte) ([]byte, error) {
	conn, err := socksConnect(proxyAddress, host, port)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.Close()
	}()
	return writeThenRead(conn, conn, message)
}

// httpConnectRoundTrip tunnels to target via an http CONNECT and echoes message.
func httpConnectRoundTrip(proxyAddress, target string, message []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", proxyAddress, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.Close()
	}()

	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.Contains(statusLine, "200") {
		return nil, fmt.Errorf("connect failed: %s", strings.TrimSpace(statusLine))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return writeThenRead(conn, reader, message)
}

func writeThenRead(writer io.Writer, reader io.Reader, message []byte) ([]byte, error) {
	if _, err := writer.Write(message); err != nil {
		return nil, err
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(reader, echo); err != nil {
		return nil, err
	}
	return echo, nil
}

// retryEcho retries the round trip until it succeeds or the deadline passes,
// so mesh tests tolerate peer discovery latency.
func retryEcho(t *testing.T, message []byte, roundTrip func() ([]byte, error)) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		echo, err := roundTrip()
		if err == nil {
			if !bytes.Equal(echo, message) {
				t.Fatalf("expected %q, got %q", message, echo)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed to echo through proxy: %s", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestGatewaySocksProxy(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	proxyAddress, closeProxy := startSocksProxy(t, instance)
	defer closeProxy()

	extensions := map[string]string{"permit-port-forwarding": ""}
	alice := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()
	exposeEchoService(t, alice, "myservice", 8000)

	message := []byte("hello through socks")
	echo, err := socksRoundTrip(proxyAddress, "myservice.alice", 8000, message)
	if err != nil {
		t.Fatalf("socks round trip failed: %s", err)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("expected %q, got %q", message, echo)
	}
}

func TestGatewaySocksProxyUnknownService(t *testing.T) {
	t.Parallel()
	_, _, instance, _, release := startTestGateway(t)
	defer release()

	proxyAddress, closeProxy := startSocksProxy(t, instance)
	defer closeProxy()

	if conn, err := socksConnect(proxyAddress, "nothing.nobody", 1234); err == nil {
		_ = conn.Close()
		t.Fatal("expected socks connect to unknown service to fail")
	}
}

func TestGatewayHTTPConnectProxy(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	proxyAddress, closeProxy := startHttpProxy(t, instance)
	defer closeProxy()

	extensions := map[string]string{"permit-port-forwarding": ""}
	alice := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()
	exposeEchoService(t, alice, "myservice", 8000)

	message := []byte("hello through connect")
	echo, err := httpConnectRoundTrip(proxyAddress, "myservice.alice:8000", message)
	if err != nil {
		t.Fatalf("http connect round trip failed: %s", err)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("expected %q, got %q", message, echo)
	}
}

// TestGatewayHTTPPlainProxy exercises the plain (non-CONNECT) http forward
// proxy path, which dials the exposed service through openServiceTunnel and
// relays the http response.
func TestGatewayHTTPPlainProxy(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	proxyAddress, closeProxy := startHttpProxy(t, instance)
	defer closeProxy()

	extensions := map[string]string{"permit-port-forwarding": ""}
	alice := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()
	exposeHttpService(t, alice, "web", 80, "hello from web")

	proxyURL, err := url.Parse("http://" + proxyAddress)
	if err != nil {
		t.Fatalf("failed to parse proxy url: %s", err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
	response, err := client.Get("http://web.alice/")
	if err != nil {
		t.Fatalf("failed to get through http proxy: %s", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("failed to read body: %s", err)
	}
	if string(body) != "hello from web" {
		t.Fatalf("unexpected body: %q", body)
	}
}

// TestGatewaySocksProxyViaMesh proves a socks proxy on one node reaches a
// service exposed on another node through the peer connection.
func TestGatewaySocksProxyViaMesh(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSignerA := newTestSigner(t)
	userCaSignerB := newTestSigner(t)

	addressA, instanceA, releaseA := startTestNode(t, database, userCaSignerA, peerCaSigner, "node-a", "")
	defer releaseA()
	addressB, _, releaseB := startTestNode(t, database, userCaSignerB, peerCaSigner, "node-b", "")
	defer releaseB()
	_ = addressA

	// alice exposes an echo service on node b
	extensions := map[string]string{"permit-port-forwarding": ""}
	alice := dialTestGateway(t, addressB, newCertSigner(t, userCaSignerB, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()
	exposeEchoService(t, alice, "myservice", 8000)

	// the socks proxy runs on node a; reaching alice's service on node b
	// exercises node a forwarding through its peer connection to node b
	proxyAddress, closeProxy := startSocksProxy(t, instanceA)
	defer closeProxy()

	message := []byte("hello across the mesh via socks")
	retryEcho(t, message, func() ([]byte, error) {
		return socksRoundTrip(proxyAddress, "myservice.alice", 8000, message)
	})
}

// TestGatewayHTTPConnectProxyViaMesh proves an http CONNECT proxy on one node
// reaches a service exposed on another node through the peer connection.
func TestGatewayHTTPConnectProxyViaMesh(t *testing.T) {
	t.Parallel()
	database, release := dbtest.AcquireDatabase(t)
	defer release()

	peerCaSigner := newTestSigner(t)
	userCaSignerA := newTestSigner(t)
	userCaSignerB := newTestSigner(t)

	_, instanceA, releaseA := startTestNode(t, database, userCaSignerA, peerCaSigner, "node-a", "")
	defer releaseA()
	addressB, _, releaseB := startTestNode(t, database, userCaSignerB, peerCaSigner, "node-b", "")
	defer releaseB()

	extensions := map[string]string{"permit-port-forwarding": ""}
	alice := dialTestGateway(t, addressB, newCertSigner(t, userCaSignerB, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()
	exposeEchoService(t, alice, "myservice", 8000)

	proxyAddress, closeProxy := startHttpProxy(t, instanceA)
	defer closeProxy()

	message := []byte("hello across the mesh via connect")
	retryEcho(t, message, func() ([]byte, error) {
		return httpConnectRoundTrip(proxyAddress, "myservice.alice:8000", message)
	})
}
