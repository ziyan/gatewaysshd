package gateway

import (
	"io"
	"io/ioutil"
	"sync"
	"time"

	"github.com/golang/glog"
	"golang.org/x/crypto/ssh"
)

type Channel struct {
	session     *Session
	channel     ssh.Channel
	channelType string
	extraData   []byte
	active      bool
	closeOnce   sync.Once
	created     time.Time
}

func NewChannel(session *Session, channel ssh.Channel, channelType string, extraData []byte) (*Channel, error) {
	return &Channel{
		session:     session,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		created:     time.Now(),
	}, nil
}

func (c *Channel) Close() {
	c.closeOnce.Do(func() {
		c.active = false
		c.Session().DeleteChannel(c)
		if err := c.channel.Close(); err != nil {
			glog.Warningf("failed to close channel: %s", err)
		}
		glog.V(1).Infof("channel closed: %s", c.Session().User())
	})
}

func (c *Channel) Session() *Session {
	return c.session
}

func (c *Channel) Created() time.Time {
	return c.created
}

func (c *Channel) HandleRequests(requests <-chan *ssh.Request) {
	defer c.Close()

	for request := range requests {
		go c.HandleRequest(request)
	}
}

func (c *Channel) HandleRequest(request *ssh.Request) {
	glog.V(2).Infof("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	// check parameters
	ok := false
	switch request.Type {
	case "env":
		// just ignore the env settings from client
		ok = true

	case "shell":
		// let client open shell without any command
		if len(request.Payload) > 0 {
			break
		}
		ok = true
	}

	// reply to client
	if request.WantReply {
		if err := request.Reply(ok, nil); err != nil {
			glog.Warningf("failed to reply to request: %s", err)
		}
	}

	// do actual work here
	switch request.Type {
	case "shell":
		defer c.Close()
		c.Session().Gateway().ListSessions(c.channel)
	}
}

func (c *Channel) HandleTunnelChannel(c2 *Channel) {

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

func (c *Channel) HandleSessionChannel() {
	defer c.Close()

	io.Copy(ioutil.Discard, c.channel)
}
