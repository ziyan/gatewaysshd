package gateway

import (
	"errors"

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
