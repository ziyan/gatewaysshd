package gateway

import (
	"errors"
	"net"

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
		return nil
	}
	defer d.Close()

	r, err := d.City(ip)
	if err != nil {
		return nil
	}

	return map[string]interface{}{
		"country":   r.Country.IsoCode,
		"city":      r.City.Names["en"],
		"latitude":  r.Location.Latitude,
		"longitude": r.Location.Longitude,
	}
}
