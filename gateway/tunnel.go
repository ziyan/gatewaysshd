package gateway

import (
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Tunnel struct {
	connection   *Connection
	channel      ssh.Channel
	channelType  string
	extraData    []byte
	active       bool
	closeOnce    sync.Once
	created      time.Time
	used         time.Time
	bytesRead    uint64
	bytesWritten uint64
	metadata     map[string]interface{}
}

func newTunnel(connection *Connection, channel ssh.Channel, channelType string, extraData []byte, metadata map[string]interface{}) (*Tunnel, error) {
	log.Infof("new tunnel: user = %s, remote = %v, type = %s, metadata = %v", connection.user, connection.remoteAddr, channelType, metadata)
	return &Tunnel{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		created:     time.Now(),
		used:        time.Now(),
		metadata:    metadata,
	}, nil
}

func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		t.connection.deleteTunnel(t)

		if err := t.channel.Close(); err != nil {
			log.Warningf("failed to close tunnel: %s", err)
		}

		log.Infof("tunnel closed: user = %s, remote = %v, type = %s, metadata = %v, read = %d, written = %d", t.connection.user, t.connection.remoteAddr, t.channelType, t.metadata, t.bytesRead, t.bytesWritten)
	})
}

func (t *Tunnel) Handle(requests <-chan *ssh.Request, t2 *Tunnel) {
	go t.handleRequests(requests)
	if t2 != nil {
		go t.handleTunnel(t2)
	}
}

func (t *Tunnel) handleRequests(requests <-chan *ssh.Request) {
	defer t.Close()

	for request := range requests {
		t.used = time.Now()
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
		io.Copy(t, t2)
		close(done1)
	}()

	done2 := make(chan struct{})
	go func() {
		io.Copy(t2, t)
		close(done2)
	}()

	select {
	case <-done1:
	case <-done2:
	}
}

func (t *Tunnel) gatherStatus() map[string]interface{} {
	status := map[string]interface{}{
		"type":          t.channelType,
		"created":       t.created.Unix(),
		"used":          t.used.Unix(),
		"bytes_read":    t.bytesRead,
		"bytes_written": t.bytesWritten,
	}
	for k, v := range t.metadata {
		status[k] = v
	}
	return status
}

func (t *Tunnel) Read(data []byte) (int, error) {
	size, err := t.channel.Read(data)
	t.bytesRead += uint64(size)
	t.used = time.Now()
	return size, err
}

func (t *Tunnel) Write(data []byte) (int, error) {
	size, err := t.channel.Write(data)
	t.bytesWritten += uint64(size)
	t.used = time.Now()
	return size, err
}
