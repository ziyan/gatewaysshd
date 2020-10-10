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

func newTunnel(connection *Connection, channel ssh.Channel, channelType string, extraData []byte, metadata map[string]interface{}) *Tunnel {
	log.Infof("new tunnel: user = %s, remote = %v, type = %s, metadata = %v", connection.user, connection.remoteAddr, channelType, metadata)
	return &Tunnel{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		metadata:    metadata,
	}
}

// close the tunnel
func (self *Tunnel) Close() {
	self.closeOnce.Do(func() {
		if err := self.channel.Close(); err != nil {
			log.Warningf("failed to close tunnel: %s", err)
		}

		log.Infof("tunnel closed: user = %s, remote = %v, type = %s, metadata = %v", self.connection.user, self.connection.remoteAddr, self.channelType, self.metadata)

		self.connection.deleteTunnel(self)
	})
}

func (self *Tunnel) handleRequests(requests <-chan *ssh.Request) {
	defer self.Close()

	for request := range requests {
		go self.handleRequest(request)
	}
}

func (self *Tunnel) handleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	// reply to client
	if request.WantReply {
		if err := request.Reply(false, nil); err != nil {
			log.Warningf("failed to reply to request: %s", err)
			return
		}
	}
}

func (self *Tunnel) handleTunnel(t2 *Tunnel) {
	defer t2.Close()
	defer self.Close()

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		io.Copy(self.channel, t2.channel)
	}()

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		io.Copy(t2.channel, self.channel)
	}()

	select {
	case <-done1:
	case <-done2:
	}
}

type tunnelStatus struct {
	Type     string                 `json:"type,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func (self *Tunnel) gatherStatus() *tunnelStatus {
	return &tunnelStatus{
		Type:     self.channelType,
		Metadata: self.metadata,
	}
}
