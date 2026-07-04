package gateway_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/gateway"
	"github.com/ziyan/gatewaysshd/util/deferutil"
)

// exposeEchoService connects the given client to the gateway and advertises an
// echo service on host:port, so proxy tests have something to reach.
func exposeEchoService(t *testing.T, client *ssh.Client, host string, port uint32) {
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
				_, _ = io.Copy(channel, channel)
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

// socksConnect performs a socks5 CONNECT handshake to host:port through the
// proxy and returns the established connection.
func socksConnect(t *testing.T, proxyAddress, host string, port uint16) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddress, 10*time.Second)
	if err != nil {
		t.Fatalf("failed to dial socks proxy: %s", err)
	}

	// method negotiation: version 5, one method, no-auth
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("failed to write socks methods: %s", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("failed to read socks method reply: %s", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("unexpected socks method reply: %v", reply)
	}

	// CONNECT request with a domain-name address
	hostLength := byte(len(host)) // #nosec G115 - test host is short
	request := []byte{0x05, 0x01, 0x00, 0x03, hostLength}
	request = append(request, []byte(host)...)
	request = binary.BigEndian.AppendUint16(request, port)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("failed to write socks request: %s", err)
	}

	// reply: version, rep, rsv, atyp(ipv4), 4-byte address, 2-byte port
	response := make([]byte, 10)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("failed to read socks reply: %s", err)
	}
	if response[1] != 0x00 {
		_ = conn.Close()
		t.Fatalf("socks connect failed with reply code %d", response[1])
	}
	return conn
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

	conn := socksConnect(t, proxyAddress, "myservice.alice", 8000)
	defer func() {
		_ = conn.Close()
	}()

	message := []byte("hello through socks")
	if _, err := conn.Write(message); err != nil {
		t.Fatalf("failed to write: %s", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("failed to read: %s", err)
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

	// a socks CONNECT to an unknown service must return a non-success reply
	conn, err := net.DialTimeout("tcp", proxyAddress, 10*time.Second)
	if err != nil {
		t.Fatalf("failed to dial socks proxy: %s", err)
	}
	defer func() {
		_ = conn.Close()
	}()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("failed to write methods: %s", err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
		t.Fatalf("failed to read method reply: %s", err)
	}
	host := "nothing.nobody"
	hostLength := byte(len(host)) // #nosec G115 - test host is short
	request := append([]byte{0x05, 0x01, 0x00, 0x03, hostLength}, []byte(host)...)
	request = binary.BigEndian.AppendUint16(request, 1234)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("failed to write request: %s", err)
	}
	response := make([]byte, 10)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("failed to read reply: %s", err)
	}
	if response[1] == 0x00 {
		t.Fatal("expected socks connect to unknown service to fail")
	}
}

func TestGatewayHTTPConnectProxy(t *testing.T) {
	t.Parallel()
	address, caSigner, instance, _, release := startTestGateway(t)
	defer release()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	proxyServer := &http.Server{Handler: instance.HTTPProxyHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer deferutil.Recover()
		_ = proxyServer.Serve(listener)
	}()
	defer func() {
		_ = proxyServer.Close()
	}()

	extensions := map[string]string{"permit-port-forwarding": ""}
	alice := dialTestGateway(t, address, newCertSigner(t, caSigner, "alice", extensions), "alice")
	defer func() {
		_ = alice.Close()
	}()
	exposeEchoService(t, alice, "myservice", 8000)

	// send an HTTP CONNECT to the proxy, then use the tunnel as raw tcp
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatalf("failed to dial http proxy: %s", err)
	}
	defer func() {
		_ = conn.Close()
	}()
	if _, err := conn.Write([]byte("CONNECT myservice.alice:8000 HTTP/1.1\r\nHost: myservice.alice:8000\r\n\r\n")); err != nil {
		t.Fatalf("failed to write CONNECT: %s", err)
	}
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read CONNECT response: %s", err)
	}
	if !bytes.Contains([]byte(statusLine), []byte("200")) {
		t.Fatalf("unexpected CONNECT response: %q", statusLine)
	}
	// consume the rest of the response headers
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read headers: %s", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	message := []byte("hello through connect")
	if _, err := conn.Write(message); err != nil {
		t.Fatalf("failed to write: %s", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("failed to read echo: %s", err)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("expected %q, got %q", message, echo)
	}
}
