package gateway

import (
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// a tunnel within a ssh connection
type tunnel struct {
	connection  *connection
	channel     ssh.Channel
	channelType string
	extraData   []byte
	done        chan struct{}
	closeOnce   sync.Once
	metadata    map[string]interface{}
}

func newTunnel(connection *connection, channel ssh.Channel, channelType string, extraData []byte, metadata map[string]interface{}) *tunnel {
	log.Infof("%s: new tunnel: type = %s, metadata = %v", connection, channelType, metadata)
	self := &tunnel{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		done:        make(chan struct{}),
		metadata:    metadata,
	}
	connection.addTunnel(self)
	return self
}

// close the tunnel
func (self *tunnel) close() {
	self.closeOnce.Do(func() {
		close(self.done)
	})
}

func (self *tunnel) handleRequests(requests <-chan *ssh.Request) {
	defer func() {
		self.connection.deleteTunnel(self)
		if err := self.channel.Close(); err != nil {
			log.Warningf("%s: failed to close tunnel: %s", self.connection, err)
		}
		log.Infof("%s: tunnel closed: type = %s, metadata = %v", self.connection, self.channelType, self.metadata)
	}()

	for {
		select {
		case <-self.done:
			return
		case request, ok := <-requests:
			if !ok {
				return
			}
			// log.Debugf("%s: request received: type = %s, want_reply = %v, payload = %d", self.connection, request.Type, request.WantReply, len(request.Payload))
			// reply to client
			if request.WantReply {
				if err := request.Reply(false, nil); err != nil {
					log.Errorf("%s: failed to reply to request: %s", self.connection, err)
					return
				}
			}
		}
	}
}

func (self *tunnel) handleTunnel(otherTunnel *tunnel) {
	defer otherTunnel.close()
	defer self.close()

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_, _ = io.Copy(self.channel, otherTunnel.channel)
	}()

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		_, _ = io.Copy(otherTunnel.channel, self.channel)
	}()

	// wait until one of them is done
	select {
	case <-done1:
	case <-done2:
	}
}

type tunnelStatus struct {
	Type     string                 `json:"type,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func (self *tunnel) gatherStatus() *tunnelStatus {
	return &tunnelStatus{
		Type:     self.channelType,
		Metadata: self.metadata,
	}
}
