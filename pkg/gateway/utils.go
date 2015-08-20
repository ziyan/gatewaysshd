package gateway

import (
	"errors"

	"golang.org/x/crypto/ssh"
)

var (
	ErrInvalidForwardRequest = errors.New("gatewaysshd: invalid forward request")
	ErrInvalidTunnelData     = errors.New("gatewaysshd: invalid tunnel data")
)

type ForwardRequest struct {
	Host string
	Port uint32
}

func UnmarshalForwardRequest(payload []byte) (*ForwardRequest, error) {
	request := &ForwardRequest{}

	if err := ssh.Unmarshal(payload, request); err != nil {
		return nil, err
	}

	// TODO: check host
	if request.Port > 65535 {
		return nil, ErrInvalidForwardRequest
	}

	return request, nil
}

type ForwardReply struct {
	Port uint32
}

func MarshalForwardReply(reply *ForwardReply) []byte {
	return ssh.Marshal(reply)
}

type TunnelData struct {
	Host          string
	Port          uint32
	OriginAddress string
	OriginPort    uint32
}

func UnmarshalTunnelData(payload []byte) (*TunnelData, error) {
	data := &TunnelData{}

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

func MarshalTunnelData(data *TunnelData) []byte {
	return ssh.Marshal(data)
}

type SessionsList []*Session

func (s SessionsList) Len() int {
	return len(s)
}

func (s SessionsList) Less(i, j int) bool {
	return s[i].Created().After(s[j].Created())
}

func (s SessionsList) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
