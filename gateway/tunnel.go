package gateway

import (
	"io"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/ziyan/gatewaysshd/util/deferutil"
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
	self := &tunnel{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		done:        make(chan struct{}),
		metadata:    metadata,
	}
	log.Infof("%s: new tunnel: type = %s, metadata = %v", self.owner(), channelType, metadata)
	if connection != nil {
		connection.addTunnel(self)
	}
	return self
}

// owner describes where the tunnel came from, peer tunnels have no connection
func (self *tunnel) owner() string {
	if self.connection != nil {
		return self.connection.String()
	}
	return "peer"
}

// close the tunnel
func (self *tunnel) close() {
	self.closeOnce.Do(func() {
		close(self.done)
	})
}

func (self *tunnel) handleRequests(requests <-chan *ssh.Request) {
	defer func() {
		if self.connection != nil {
			self.connection.deleteTunnel(self)
		}
		if err := self.channel.Close(); err != nil {
			log.Warningf("%s: failed to close tunnel: %s", self.owner(), err)
		}
		log.Infof("%s: tunnel closed: type = %s, metadata = %v", self.owner(), self.channelType, self.metadata)
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
					log.Errorf("%s: failed to reply to request: %s", self.owner(), err)
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
		defer deferutil.Recover()
		defer close(done1)
		_, _ = io.Copy(self.channel, otherTunnel.channel)
	}()

	done2 := make(chan struct{})
	go func() {
		defer deferutil.Recover()
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
