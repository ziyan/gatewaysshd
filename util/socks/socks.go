// Package socks implements the parts of the SOCKS protocol version 5 needed to
// accept CONNECT requests. See https://tools.ietf.org/html/rfc1928.
package socks

import (
	"errors"
	"io"
	"net"

	logging "github.com/op/go-logging"
)

const (
	socksVersion = byte(5)

	noAuthRequired     = byte(0)
	noAcceptableMethod = byte(255)

	connectCommand = byte(1)

	ipv4AddressType       = byte(1)
	domainNameAddressType = byte(3)
	ipv6AddressType       = byte(4)
)

// Reply is a SOCKS5 reply code.
type Reply byte

const (
	Succeeded               = Reply(0)
	ServerFailure           = Reply(1)
	NotAllowed              = Reply(2)
	NetworkUnreachable      = Reply(3)
	HostUnreachable         = Reply(4)
	ConnectionRefused       = Reply(5)
	TTLExpired              = Reply(6)
	CommandNotSupported     = Reply(7)
	AddressTypeNotSupported = Reply(8)
)

var (
	ErrUnsupportedVersion = errors.New("socks: unsupported version")
	ErrUnsupportedAuth    = errors.New("socks: unsupported auth")
	ErrUnsupportedAddress = errors.New("socks: unsupported address")
	ErrUnsupportedCommand = errors.New("socks: unsupported command")
)

var log = logging.MustGetLogger("socks")

// Authenticate performs the SOCKS5 method negotiation, accepting only the
// no-authentication method (the proxy itself is unauthenticated).
func Authenticate(conn net.Conn) error {
	header := []byte{0, 0}
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	if header[0] != socksVersion {
		log.Errorf("received invalid version: %v", header[0])
		return ErrUnsupportedVersion
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == noAuthRequired {
			if _, err := conn.Write([]byte{socksVersion, noAuthRequired}); err != nil {
				return err
			}
			return nil
		}
	}
	log.Errorf("no authentication method acceptable: %v", methods)
	_, _ = conn.Write([]byte{socksVersion, noAcceptableMethod})
	return ErrUnsupportedAuth
}

// ParseRequest reads a SOCKS5 request and returns the requested host and port.
// Only the CONNECT command is supported. IPv4, IPv6, and domain-name address
// types are all accepted; the returned host is the literal address string.
func ParseRequest(conn net.Conn) (string, uint16, error) {
	header := []byte{0, 0, 0, 0}
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", 0, err
	}
	if header[0] != socksVersion {
		log.Errorf("received invalid version: %v", header[0])
		return "", 0, ErrUnsupportedVersion
	}

	var address string
	switch header[3] {
	case domainNameAddressType:
		length := []byte{0}
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", 0, err
		}
		addressBytes := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, addressBytes); err != nil {
			return "", 0, err
		}
		address = string(addressBytes)
	case ipv4AddressType:
		addressBytes := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, addressBytes); err != nil {
			return "", 0, err
		}
		address = net.IP(addressBytes).String()
	case ipv6AddressType:
		addressBytes := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, addressBytes); err != nil {
			return "", 0, err
		}
		address = net.IP(addressBytes).String()
	default:
		return "", 0, ErrUnsupportedAddress
	}

	portBytes := []byte{0, 0}
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", 0, err
	}
	port := (uint16(portBytes[0]) << 8) | uint16(portBytes[1])

	if header[1] != connectCommand {
		_ = SendReply(conn, CommandNotSupported)
		return "", 0, ErrUnsupportedCommand
	}

	return address, port, nil
}

// SendReply writes a SOCKS5 reply with an all-zero bound address.
func SendReply(conn net.Conn, reply Reply) error {
	if _, err := conn.Write([]byte{
		socksVersion,
		byte(reply),
		0,               // reserved
		ipv4AddressType, // bound address type
		0, 0, 0, 0,      // bound address
		0, 0, // bound port
	}); err != nil {
		log.Warningf("failed to send reply on socks connection: %s", err)
		return err
	}
	return nil
}
