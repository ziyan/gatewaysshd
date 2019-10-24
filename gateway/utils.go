package gateway

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/oschwald/geoip2-golang"
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

type forwardReply struct {
	Port uint32
}

func marshalForwardReply(reply *forwardReply) []byte {
	return ssh.Marshal(reply)
}

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

type executeRequest struct {
	Command string
}

func unmarshalExecuteRequest(payload []byte) (*executeRequest, error) {
	request := &executeRequest{}

	if err := ssh.Unmarshal(payload, request); err != nil {
		return nil, err
	}

	return request, nil
}

func lookupLocation(db string, ip net.IP) map[string]interface{} {
	d, err := geoip2.Open(db)
	if err != nil {
		log.Warningf("failed to open geoip database file %s: %s", db, err)
		return nil
	}
	defer d.Close()

	r, err := d.City(ip)
	if err != nil {
		return nil
	}

	if r.Country.IsoCode == "" {
		return nil
	}

	if r.Location.Latitude == 0 && r.Location.Longitude == 0 {
		return nil
	}

	location := map[string]interface{}{
		"country":   r.Country.IsoCode,
		"latitude":  r.Location.Latitude,
		"longitude": r.Location.Longitude,
	}
	if r.City.Names["en"] != "" {
		location["city"] = r.City.Names["en"]
	}
	if r.Location.TimeZone != "" {
		location["timezone"] = r.Location.TimeZone
	}
	if len(r.Subdivisions) > 0 && r.Subdivisions[0].Names["en"] != "" {
		location["subdivision"] = r.Subdivisions[0].Names["en"]
	}
	return location
}

type usageStats struct {
	bytesRead    uint64
	bytesWritten uint64
	created      time.Time
	used         time.Time
}

func newUsage() *usageStats {
	return &usageStats{
		created: time.Now(),
		used:    time.Now(),
	}
}

func (u *usageStats) read(bytesRead uint64) {
	u.update(bytesRead, 0)
}

func (u *usageStats) write(bytesWritten uint64) {
	u.update(0, bytesWritten)
}

func (u *usageStats) use() {
	u.update(0, 0)
}

func (u *usageStats) update(bytesRead, bytesWritten uint64) {
	if bytesRead > 0 {
		atomic.AddUint64(&u.bytesRead, bytesRead)
	}
	if bytesWritten > 0 {
		atomic.AddUint64(&u.bytesWritten, bytesWritten)
	}
	u.used = time.Now()
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
func (c *wrappedConn) Read(data []byte) (int, error) {
	size, err := c.conn.Read(data)
	if err == nil {
		c.usage.read(uint64(size))
	}
	return size, err
}

// override write to keep track of data usage
func (c *wrappedConn) Write(data []byte) (int, error) {
	size, err := c.conn.Write(data)
	if err == nil {
		c.usage.write(uint64(size))
	}
	return size, err
}

func (c *wrappedConn) Close() error {
	return c.conn.Close()
}

func (c *wrappedConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *wrappedConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *wrappedConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

func (c *wrappedConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *wrappedConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
