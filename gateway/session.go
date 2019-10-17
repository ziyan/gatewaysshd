package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"sync"

	"golang.org/x/crypto/ssh"
)

// a session within a ssh connection
type Session struct {
	connection  *Connection
	channel     ssh.Channel
	channelType string
	extraData   []byte
	closeOnce   sync.Once
}

func newSession(connection *Connection, channel ssh.Channel, channelType string, extraData []byte) (*Session, error) {
	log.Debugf("new session: user = %s, remote = %v, type = %s", connection.user, connection.remoteAddr, channelType)
	return &Session{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
	}, nil
}

// close the session
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		if err := s.channel.CloseWrite(); err != nil {
			log.Warningf("failed to close session: %s", err)
		}

		if _, err := s.channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0}); err != nil {
			log.Warningf("failed to send exit-status for session: %s", err)
		}

		if err := s.channel.Close(); err != nil {
			log.Warningf("failed to close session: %s", err)
		}

		log.Debugf("session closed: user = %s, remote = %v, type = %s", s.connection.user, s.connection.remoteAddr, s.channelType)

		s.connection.deleteSession(s)
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

		if !bytes.Equal(status, []byte("null")) {
			s.connection.reportStatus(status)
		}
	}
}

func (s *Session) gatherStatus() map[string]interface{} {
	return map[string]interface{}{
		"type": s.channelType,
	}
}
