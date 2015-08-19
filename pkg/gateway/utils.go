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
