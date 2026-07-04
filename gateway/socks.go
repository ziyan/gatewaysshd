package gateway

import (
	"io"
	"net"

	"github.com/ziyan/gatewaysshd/util/deferutil"
	"github.com/ziyan/gatewaysshd/util/socks"
)

// HandleSocksConnection serves a single SOCKS5 client. The proxy is
// unauthenticated: any client that reaches the listener can open a tunnel to
// any exposed service, the same reachability as an "ssh -D" dynamic forward.
// It should therefore only be bound to a trusted network.
func (self *gateway) HandleSocksConnection(socket net.Conn) {
	log.Infof("socks connection accepted: remote = %s, local = %s", socket.RemoteAddr(), socket.LocalAddr())
	defer func() {
		if err := socket.Close(); err != nil {
			log.Warningf("failed to close socks connection: %s", err)
		}
		log.Infof("socks connection closed: remote = %s", socket.RemoteAddr())
	}()

	if err := socks.Authenticate(socket); err != nil {
		log.Warningf("failed during socks handshake: %s", err)
		return
	}

	host, port, err := socks.ParseRequest(socket)
	if err != nil {
		log.Warningf("failed during socks request: %s", err)
		return
	}

	originAddress, originPort := splitOrigin(socket.RemoteAddr())
	tunnel, err := self.openServiceTunnel(host, port, originAddress, originPort)
	if err != nil {
		log.Warningf("socks: failed to open tunnel to %s:%d: %s", host, port, err)
		_ = socks.SendReply(socket, socks.HostUnreachable)
		return
	}
	defer tunnel.close()

	if err := socks.SendReply(socket, socks.Succeeded); err != nil {
		return
	}

	bridge(socket, tunnel.channel)
}

// splitOrigin extracts the ip and port of a tcp remote address for tunnel
// origin metadata, tolerating non-tcp addresses.
func splitOrigin(address net.Addr) (string, uint32) {
	if tcpAddress, ok := address.(*net.TCPAddr); ok {
		// #nosec G115 - a tcp port is always within uint32 range
		return tcpAddress.IP.String(), uint32(tcpAddress.Port)
	}
	return address.String(), 0
}

// bridge copies bytes in both directions until either side closes.
func bridge(left io.ReadWriter, right io.ReadWriter) {
	done1 := make(chan struct{})
	go func() {
		defer deferutil.Recover()
		defer close(done1)
		_, _ = io.Copy(left, right)
	}()

	done2 := make(chan struct{})
	go func() {
		defer deferutil.Recover()
		defer close(done2)
		_, _ = io.Copy(right, left)
	}()

	select {
	case <-done1:
	case <-done2:
	}
}
