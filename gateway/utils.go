package gateway

import (
	"encoding/csv"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ErrInvalidForwardRequest = errors.New("gatewaysshd: invalid forward request")
	ErrInvalidTunnelData     = errors.New("gatewaysshd: invalid tunnel data")
)

type forwardRequest struct {
	Host string
	Port uint32
}

func unmarshalForwardRequest(payload []byte) (*forwardRequest, error) {
	request := &forwardRequest{}

	if err := ssh.Unmarshal(payload, request); err != nil {
		return nil, err
	}

	// TODO: check host
	if request.Port > 65535 {
		return nil, ErrInvalidForwardRequest
	}

	return request, nil
}

// type forwardReply struct {
// 	Port uint32
// }

// func marshalForwardReply(reply *forwardReply) []byte {
// 	return ssh.Marshal(reply)
// }

type tunnelData struct {
	Host          string
	Port          uint32
	OriginAddress string
	OriginPort    uint32
}

func unmarshalTunnelData(payload []byte) (*tunnelData, error) {
	data := &tunnelData{}

	if err := ssh.Unmarshal(payload, data); err != nil {
		return nil, err
	}

	// TODO: check host
	if data.Port > 65535 {
		return nil, ErrInvalidTunnelData
	}

	if data.OriginPort > 65535 {
		return nil, ErrInvalidTunnelData
	}

	return data, nil
}

func marshalTunnelData(data *tunnelData) []byte {
	return ssh.Marshal(data)
}

// type executeRequest struct {
// 	Command string
// }

// func unmarshalExecuteRequest(payload []byte) (*executeRequest, error) {
// 	request := &executeRequest{}

// 	if err := ssh.Unmarshal(payload, request); err != nil {
// 		return nil, err
// 	}

// 	return request, nil
// }

type usageStats struct {
	bytesRead    uint64
	bytesWritten uint64
	createdAt    time.Time
	usedAt       time.Time
}

func newUsage() *usageStats {
	return &usageStats{
		createdAt: time.Now(),
		usedAt:    time.Now(),
	}
}

func (u *usageStats) read(bytesRead uint64) {
	u.update(bytesRead, 0)
}

func (u *usageStats) write(bytesWritten uint64) {
	u.update(0, bytesWritten)
}

// func (u *usageStats) use() {
// 	u.update(0, 0)
// }

func (u *usageStats) update(bytesRead, bytesWritten uint64) {
	if bytesRead > 0 {
		atomic.AddUint64(&u.bytesRead, bytesRead)
	}
	if bytesWritten > 0 {
		atomic.AddUint64(&u.bytesWritten, bytesWritten)
	}
	u.usedAt = time.Now()
}

type wrappedConn struct {
	conn  net.Conn
	usage *usageStats
}

func wrapConn(conn net.Conn, usage *usageStats) *wrappedConn {
	return &wrappedConn{
		conn:  conn,
		usage: usage,
	}
}

// override read to keep track of data usage
func (self *wrappedConn) Read(data []byte) (int, error) {
	size, err := self.conn.Read(data)
	if err == nil && size >= 0 {
		self.usage.read(uint64(size)) // safe: size from Read is always non-negative
	}
	return size, err
}

// override write to keep track of data usage
func (self *wrappedConn) Write(data []byte) (int, error) {
	size, err := self.conn.Write(data)
	if err == nil && size >= 0 {
		self.usage.write(uint64(size)) // safe: size from Write is always non-negative
	}
	return size, err
}

func (self *wrappedConn) Close() error {
	return self.conn.Close()
}

func (self *wrappedConn) LocalAddr() net.Addr {
	return self.conn.LocalAddr()
}

func (self *wrappedConn) RemoteAddr() net.Addr {
	return self.conn.RemoteAddr()
}

func (self *wrappedConn) SetDeadline(t time.Time) error {
	return self.conn.SetDeadline(t)
}

func (self *wrappedConn) SetReadDeadline(t time.Time) error {
	return self.conn.SetReadDeadline(t)
}

func (self *wrappedConn) SetWriteDeadline(t time.Time) error {
	return self.conn.SetWriteDeadline(t)
}

func splitCommand(command string) []string {
	reader := csv.NewReader(strings.NewReader(command))
	reader.Comma = ' '
	fields, err := reader.Read()
	if err != nil {
		return nil
	}
	return fields
}
