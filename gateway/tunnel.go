package gateway

import (
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// a tunnel within a ssh connection
type Tunnel struct {
	connection  *Connection
	channel     ssh.Channel
	channelType string
	extraData   []byte
	active      bool
	closeOnce   sync.Once
	metadata    map[string]interface{}
}

func newTunnel(connection *Connection, channel ssh.Channel, channelType string, extraData []byte, metadata map[string]interface{}) (*Tunnel, error) {
	log.Infof("new tunnel: user = %s, remote = %v, type = %s, metadata = %v", connection.user, connection.remoteAddr, channelType, metadata)
	return &Tunnel{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		metadata:    metadata,
	}, nil
}

// close the tunnel
func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		if err := t.channel.Close(); err != nil {
			log.Warningf("failed to close tunnel: %s", err)
		}

		log.Infof("tunnel closed: user = %s, remote = %v, type = %s, metadata = %v", t.connection.user, t.connection.remoteAddr, t.channelType, t.metadata)

		t.connection.deleteTunnel(t)
	})
}

func (t *Tunnel) handle(requests <-chan *ssh.Request, t2 *Tunnel) {
	go t.handleRequests(requests)
	if t2 != nil {
		go t.handleTunnel(t2)
	}
}

func (t *Tunnel) handleRequests(requests <-chan *ssh.Request) {
	defer t.Close()

	for request := range requests {
		go t.handleRequest(request)
	}
}

func (t *Tunnel) handleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	// reply to client
	if request.WantReply {
		if err := request.Reply(false, nil); err != nil {
			log.Warningf("failed to reply to request: %s", err)
			return
		}
	}
}

func (t *Tunnel) handleTunnel(t2 *Tunnel) {
	defer t2.Close()
	defer t.Close()

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		io.Copy(t.channel, t2.channel)
	}()

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		io.Copy(t2.channel, t.channel)
	}()

	select {
	case <-done1:
	case <-done2:
	}
}

func (t *Tunnel) gatherStatus() map[string]interface{} {
	status := map[string]interface{}{
		"type": t.channelType,
	}
	for k, v := range t.metadata {
		status[k] = v
	}
	return status
}
