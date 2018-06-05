package gateway

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Session struct {
	connection   *Connection
	channel      ssh.Channel
	channelType  string
	extraData    []byte
	closeOnce    sync.Once
	created      time.Time
	used         time.Time
	bytesRead    uint64
	bytesWritten uint64
}

func newSession(connection *Connection, channel ssh.Channel, channelType string, extraData []byte) (*Session, error) {
	log.Debugf("new session: user = %s, remote = %v, type = %s", connection.user, connection.remoteAddr, channelType)
	return &Session{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
		created:     time.Now(),
		used:        time.Now(),
	}, nil
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.connection.deleteSession(s)

		if err := s.channel.Close(); err != nil {
			log.Warningf("failed to close session: %s", err)
		}

		log.Debugf("session closed: user = %s, remote = %v, type = %s, read = %d, written = %d", s.connection.user, s.connection.remoteAddr, s.channelType, s.bytesRead, s.bytesWritten)
	})
}

func (s *Session) Handle(requests <-chan *ssh.Request) {
	go s.handleRequests(requests)
	go s.handleChannel()
}

func (s *Session) handleChannel() {
	defer s.Close()

	io.Copy(ioutil.Discard, s)
}

func (s *Session) handleRequests(requests <-chan *ssh.Request) {
	defer s.Close()

	for request := range requests {
		s.used = time.Now()
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
			status = s.connection.gatherStatus(true)
		} else {
			status = s.connection.gateway.gatherStatus(true)
		}

		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			log.Warningf("failed to marshal status: %s", err)
			break
		}

		if _, err := s.Write(encoded); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}

		if _, err := s.Write([]byte("\n")); err != nil {
			log.Warningf("failed to send status: %s", err)
			break
		}

	case "exec":
		defer s.Close()

		// send response
		if _, err := s.Write([]byte("{}\n")); err != nil {
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

		// save a copy of the status on disk
		if err := s.connection.writeStatus(); err != nil {
			log.Warningf("failed to write status for %s: %s", s.connection.user, err)
			break
		}
	}
}

func (s *Session) gatherStatus() map[string]interface{} {
	return map[string]interface{}{
		"type":          s.channelType,
		"created":       s.created.Unix(),
		"used":          s.used.Unix(),
		"bytes_read":    s.bytesRead,
		"bytes_written": s.bytesWritten,
	}
}

func (s *Session) Read(data []byte) (int, error) {
	size, err := s.channel.Read(data)
	s.bytesRead += uint64(size)
	s.used = time.Now()
	return size, err
}

func (s *Session) Write(data []byte) (int, error) {
	size, err := s.channel.Write(data)
	s.bytesWritten += uint64(size)
	s.used = time.Now()
	return size, err
}
