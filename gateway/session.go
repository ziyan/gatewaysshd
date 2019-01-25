package gateway

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"sync"

	"golang.org/x/crypto/ssh"
)

// a session within a ssh connection
type Session struct {
	connection  *Connection
	channel     *wrappedChannel
	channelType string
	extraData   []byte
	closeOnce   sync.Once
	usage       *usageStats
}

func newSession(connection *Connection, channel ssh.Channel, channelType string, extraData []byte) (*Session, error) {
	log.Debugf("new session: user = %s, remote = %v, type = %s", connection.user, connection.remoteAddr, channelType)
	usage := newUsage(connection.usage)
	return &Session{
		connection:  connection,
		channel:     wrapChannel(channel, usage),
		channelType: channelType,
		extraData:   extraData,
		usage:       usage,
	}, nil
}

// close the session
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.connection.deleteSession(s)

		if err := s.channel.Close(); err != nil {
			log.Warningf("failed to close session: %s", err)
		}

		bytesRead, bytesWritten, _, _ := s.usage.get()
		log.Debugf("session closed: user = %s, remote = %v, type = %s, read = %d, written = %d", s.connection.user, s.connection.remoteAddr, s.channelType, bytesRead, bytesWritten)
	})
}

func (s *Session) handle(requests <-chan *ssh.Request) {
	go s.handleRequests(requests)
	go s.handleChannel()
}

func (s *Session) handleChannel() {
	defer s.Close()

	io.Copy(ioutil.Discard, s.channel)
}

func (s *Session) handleRequests(requests <-chan *ssh.Request) {
	defer s.Close()

	for request := range requests {
		s.usage.use()
		go s.handleRequest(request)
	}
}

func (s *Session) handleRequest(request *ssh.Request) {
	log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

	// check parameters
	ok := false
	switch request.Type {
	case "env":
		// just ignore the env settings from client
		ok = true

	case "shell":
		// allow creating shell
		ok = true

	case "exec":
		// allow execute command
		ok = true
	}

	// reply to client
	if request.WantReply {
		if err := request.Reply(ok, nil); err != nil {
			log.Warningf("failed to reply to request: %s", err)
			return
		}
	}

	// do actual work here
	switch request.Type {
	case "shell":
		defer s.Close()

		var status map[string]interface{}
		if !s.connection.admin {
			status = s.connection.gatherStatus()
		} else {
			status = s.connection.gateway.gatherStatus()
		}

		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			log.Warningf("failed to marshal status: %s", err)
			break
		}

		if _, err := s.channel.Write(encoded); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}

		if _, err := s.channel.Write([]byte("\n")); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}

	case "exec":
		defer s.Close()

		// send response
		if _, err := s.channel.Write([]byte("{}\n")); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}

		r, err := unmarshalExecuteRequest(request.Payload)
		if err != nil {
			log.Warningf("invalid payload: %s", err)
			break
		}

		var status json.RawMessage
		if err := json.Unmarshal([]byte(r.Command), &status); err != nil {
			log.Warningf("failed to unmarshal json: %s", err)
			break
		}
		s.connection.reportStatus(status)
	}
}

func (s *Session) gatherStatus() map[string]interface{} {
	bytesRead, bytesWritten, created, used := s.usage.get()
	return map[string]interface{}{
		"type":          s.channelType,
		"created":       created.Unix(),
		"used":          used.Unix(),
		"bytes_read":    bytesRead,
		"bytes_written": bytesWritten,
	}
}
