package gateway

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Channel struct {
	session      *Session
	channel      ssh.Channel
	channelType  string
	extraData    []byte
	active       bool
	closeOnce    sync.Once
	created      time.Time
	used         time.Time
	bytesRead    uint64
	bytesWritten uint64
}

func NewChannel(session *Session, channel ssh.Channel, channelType string, extraData []byte) (*Channel, error) {
	log.Infof("new channel: user = %s, remote = %v, type = %s", session.User(), session.RemoteAddr(), channelType)
	return &Channel{
		session:      session,
		channel:      channel,
		channelType:  channelType,
		extraData:    extraData,
		created:      time.Now(),
		used:         time.Now(),
		bytesRead:    0,
		bytesWritten: 0,
	}, nil
}

func (c *Channel) Close() {
	c.closeOnce.Do(func() {
		c.active = false
		c.Session().DeleteChannel(c)

		if err := c.channel.Close(); err != nil {
			log.Warningf("failed to close channel: %s", err)
		}

		log.Infof("channel closed: user = %s, remote = %v, type = %s", c.Session().User(), c.Session().RemoteAddr(), c.channelType)
	})
}

func (c *Channel) Session() *Session {
	return c.session
}

func (c *Channel) Created() time.Time {
	return c.created
}

func (c *Channel) Used() time.Time {
	return c.used
}

func (c *Channel) BytesRead() uint64 {
	return c.bytesRead
}

func (c *Channel) BytesWritten() uint64 {
	return c.bytesWritten
}

func (c *Channel) HandleRequests(requests <-chan *ssh.Request) {
	defer c.Close()

	for request := range requests {
		c.used = time.Now()
		go c.HandleRequest(request)
	}
}

func (c *Channel) HandleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

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
			log.Warningf("failed to reply to request: %s", err)
		}
	}

	// do actual work here
	switch request.Type {
	case "shell":
		defer c.Close()
		status := c.Session().Gateway().Status()
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			log.Warningf("failed to marshal status: %s", err)
			break
		}

		if _, err := c.Write(encoded); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}

		if _, err := c.Write([]byte("\n")); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}
	}
}

func (c *Channel) HandleTunnelChannel(c2 *Channel) {

	// pipe the tunnels
	go func() {
		defer c2.Close()
		defer c.Close()
		io.Copy(c, c2)
	}()

	go func() {
		defer c2.Close()
		defer c.Close()
		io.Copy(c2, c)
	}()
}

func (c *Channel) HandleSessionChannel() {
	defer c.Close()

	io.Copy(ioutil.Discard, c)
}

func (c *Channel) Read(data []byte) (int, error) {
	size, err := c.channel.Read(data)
	c.bytesRead += uint64(size)
	c.used = time.Now()
	log.Debugf("read: size = %d", size)
	return size, err
}

func (c *Channel) Write(data []byte) (int, error) {
	size, err := c.channel.Write(data)
	c.bytesWritten += uint64(size)
	c.used = time.Now()
	log.Debugf("write: size = %d", size)
	return size, err
}
