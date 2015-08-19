package gateway

import (
	"io"
	"sync"

	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

type Channel struct {
	session     *Session
	channel     ssh.Channel
	channelType string
	extraData   []byte
	active      bool
	close       sync.Once
}

func NewChannel(session *Session, channel ssh.Channel, channelType string, extraData []byte) (*Channel, error) {
	return &Channel{
		session:     session,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
	}, nil
}

func (c *Channel) HandleRequests(requests <-chan *ssh.Request) {
	defer c.Close()
	for request := range requests {

		// for other requests, ignore or reject them all
		glog.Infof("gatewaysshd: unknown request received on channel: %s", request.Type)
		if request.WantReply {
			request.Reply(false, nil)
		}
	}
}

func (c *Channel) HandleTunnel(c2 *Channel) {

	// pipe the tunnels
	go func() {
		defer c2.Close()
		defer c.Close()
		io.Copy(c.channel, c2.channel)

	}()

	go func() {
		defer c2.Close()
		defer c.Close()
		io.Copy(c2.channel, c.channel)
	}()
}

func (c *Channel) Close() {
	c.close.Do(func() {
		c.active = false
		if err := c.channel.Close(); err != nil {
			glog.Warningf("failed to close channel: %s", err)
		}
		glog.Infof("gatewaysshd: a channel has been closed for session: %s", c.session.User())
	})
}
