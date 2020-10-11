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
	active      bool
	closeOnce   sync.Once
	metadata    map[string]interface{}
}

func newTunnel(connection *connection, channel ssh.Channel, channelType string, extraData []byte, metadata map[string]interface{}) *tunnel {
	log.Infof("new tunnel: user = %s, remote = %v, type = %s, metadata = %v", connection.user, connection.remoteAddr, channelType, metadata)
	return &tunnel{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		metadata:    metadata,
	}
}

// close the tunnel
func (self *tunnel) close() {
	self.closeOnce.Do(func() {
		if err := self.channel.Close(); err != nil {
			log.Warningf("failed to close tunnel: %s", err)
		}

		log.Infof("tunnel closed: user = %s, remote = %v, type = %s, metadata = %v", self.connection.user, self.connection.remoteAddr, self.channelType, self.metadata)

		self.connection.deleteTunnel(self)
	})
}

func (self *tunnel) handleRequests(requests <-chan *ssh.Request) {
	defer self.close()

	for request := range requests {
		log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

		// reply to client
		if request.WantReply {
			if err := request.Reply(false, nil); err != nil {
				log.Warningf("failed to reply to request: %s", err)
			}
		}
	}
}

func (self *tunnel) handleTunnel(otherTunnel *tunnel) {
	defer otherTunnel.close()
	defer self.close()

	var waitGroup sync.WaitGroup
	defer waitGroup.Wait()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		io.Copy(self.channel, otherTunnel.channel)
	}()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		io.Copy(otherTunnel.channel, self.channel)
	}()
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
