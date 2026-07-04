package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// HTTPProxyHandler returns a forward-proxy handler: the CONNECT method tunnels
// arbitrary tcp (like "ssh -D"), and absolute-form requests are forwarded as a
// plain http proxy. Like the socks proxy it is unauthenticated and reaches any
// exposed service, so it should only be bound to a trusted network.
func (self *gateway) HTTPProxyHandler() http.Handler {
	reverseProxy := &httputil.ReverseProxy{
		// the request already carries an absolute target url in forward-proxy
		// form, so no rewriting is needed
		Director: func(request *http.Request) {},
		Transport: &http.Transport{
			DialContext:         self.dialServiceContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodConnect {
			self.handleHttpConnect(response, request)
			return
		}
		if !request.URL.IsAbs() {
			http.Error(response, "gatewaysshd: proxy requires an absolute request uri", http.StatusBadRequest)
			return
		}
		reverseProxy.ServeHTTP(response, request)
	})
}

// handleHttpConnect tunnels a CONNECT request to the requested service.
func (self *gateway) handleHttpConnect(response http.ResponseWriter, request *http.Request) {
	host, port, err := splitHostPort(request.Host, 443)
	if err != nil {
		http.Error(response, "gatewaysshd: invalid connect target", http.StatusBadRequest)
		return
	}

	originAddress, originPort := splitOrigin(remoteAddress(request))
	tunnel, err := self.openServiceTunnel(host, port, originAddress, originPort)
	if err != nil {
		log.Warningf("http connect: failed to open tunnel to %s:%d: %s", host, port, err)
		http.Error(response, "gatewaysshd: host unreachable", http.StatusBadGateway)
		return
	}
	defer tunnel.close()

	hijacker, ok := response.(http.Hijacker)
	if !ok {
		http.Error(response, "gatewaysshd: connect not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Warningf("http connect: failed to hijack connection: %s", err)
		return
	}
	defer func() {
		_ = clientConn.Close()
	}()

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	bridge(clientConn, tunnel.channel)
}

// dialServiceContext resolves an absolute-form http proxy request to a tunnel,
// used by the reverse proxy transport for plain (non-CONNECT) http.
func (self *gateway) dialServiceContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := splitHostPort(address, 80)
	if err != nil {
		return nil, err
	}
	tunnel, err := self.openServiceTunnel(host, port, "http-proxy", 0)
	if err != nil {
		return nil, err
	}
	return &channelConn{channel: tunnel.channel, tunnel: tunnel}, nil
}

func remoteAddress(request *http.Request) net.Addr {
	if host, portString, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		if port, err := strconv.Atoi(portString); err == nil {
			return &net.TCPAddr{IP: net.ParseIP(host), Port: port}
		}
	}
	return &net.TCPAddr{}
}

// splitHostPort splits host:port, defaulting the port when absent.
func splitHostPort(hostPort string, defaultPort uint16) (string, uint16, error) {
	host, portString, err := net.SplitHostPort(hostPort)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			return hostPort, defaultPort, nil
		}
		return "", 0, err
	}
	port, err := strconv.ParseUint(portString, 10, 16)
	if err != nil {
		return "", 0, err
	}
	return host, uint16(port), nil
}

// channelConn adapts an ssh channel to net.Conn for the reverse proxy
// transport. Deadlines are unsupported by ssh channels and are no-ops.
type channelConn struct {
	channel ssh.Channel
	tunnel  *tunnel
}

func (self *channelConn) Read(data []byte) (int, error)  { return self.channel.Read(data) }
func (self *channelConn) Write(data []byte) (int, error) { return self.channel.Write(data) }

func (self *channelConn) Close() error {
	self.tunnel.close()
	return nil
}

func (self *channelConn) LocalAddr() net.Addr                      { return &net.TCPAddr{} }
func (self *channelConn) RemoteAddr() net.Addr                     { return &net.TCPAddr{} }
func (self *channelConn) SetDeadline(deadline time.Time) error     { return nil }
func (self *channelConn) SetReadDeadline(deadline time.Time) error { return nil }
func (self *channelConn) SetWriteDeadline(deadline time.Time) error {
	return nil
}
