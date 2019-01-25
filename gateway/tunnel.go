package gateway

import (
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// a tunnel within a ssh connection
type Tunnel struct {
	connection  *Connection
	channel     *wrappedChannel
	channelType string
	extraData   []byte
	active      bool
	closeOnce   sync.Once
	usage       *usageStats
	metadata    map[string]interface{}
}

func newTunnel(connection *Connection, channel ssh.Channel, channelType string, extraData []byte, metadata map[string]interface{}) (*Tunnel, error) {
	log.Infof("new tunnel: user = %s, remote = %v, type = %s, metadata = %v", connection.user, connection.remoteAddr, channelType, metadata)
	usage := newUsage(connection.usage)
	return &Tunnel{
		connection:  connection,
		channel:     wrapChannel(channel, usage),
		channelType: channelType,
		extraData:   extraData,
		usage:       usage,
		metadata:    metadata,
	}, nil
}

// close the tunnel
func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		t.connection.deleteTunnel(t)

		if err := t.channel.Close(); err != nil {
			log.Warningf("failed to close tunnel: %s", err)
		}

		bytesRead, bytesWritten, _, _ := t.usage.get()
		log.Infof("tunnel closed: user = %s, remote = %v, type = %s, metadata = %v, read = %d, written = %d", t.connection.user, t.connection.remoteAddr, t.channelType, t.metadata, bytesRead, bytesWritten)
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
		t.usage.use()
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
		io.Copy(t.channel, t2.channel)
		close(done1)
	}()

	done2 := make(chan struct{})
	go func() {
		io.Copy(t2.channel, t.channel)
		close(done2)
	}()

	select {
	case <-done1:
	case <-done2:
	}
}

func (t *Tunnel) gatherStatus() map[string]interface{} {
	bytesRead, bytesWritten, created, used := t.usage.get()
	status := map[string]interface{}{
		"type":          t.channelType,
		"created":       created.Unix(),
		"used":          used.Unix(),
		"bytes_read":    bytesRead,
		"bytes_written": bytesWritten,
	}
	for k, v := range t.metadata {
		status[k] = v
	}
	return status
}
