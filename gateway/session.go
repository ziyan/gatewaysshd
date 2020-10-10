package gateway

import (
	"compress/gzip"
	"encoding/json"
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

func newSession(connection *Connection, channel ssh.Channel, channelType string, extraData []byte) *Session {
	log.Debugf("new session: user = %s, remote = %v, type = %s", connection.user, connection.remoteAddr, channelType)
	return &Session{
		connection:  connection,
		channel:     channel,
		channelType: channelType,
		extraData:   extraData,
	}
}

// close the session
func (self *Session) Close() {
	self.closeOnce.Do(func() {
		if err := self.channel.CloseWrite(); err != nil {
			log.Warningf("failed to close session: %s", err)
		}

		if _, err := self.channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0}); err != nil {
			log.Warningf("failed to send exit-status for session: %s", err)
		}

		if err := self.channel.Close(); err != nil {
			log.Warningf("failed to close session: %s", err)
		}

		log.Debugf("session closed: user = %s, remote = %v, type = %s", self.connection.user, self.connection.remoteAddr, self.channelType)

		self.connection.deleteSession(self)
	})
}

func (self *Session) handleRequests(requests <-chan *ssh.Request) {
	defer self.Close()

	for request := range requests {
		go self.handleRequest(request)
	}
}

func (self *Session) handleRequest(request *ssh.Request) {
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
		self.status()

	case "exec":
		r, err := unmarshalExecuteRequest(request.Payload)
		if err != nil {
			log.Warningf("invalid payload: %s", err)
			break
		}

		switch r.Command {
		case "ping":
			self.ping()
		case "status":
			self.status()
		case "reportStatus":
			self.reportStatus()
		default:
			defer self.Close()

			// legacy behavior, command itself is json
			var status json.RawMessage
			if err := json.Unmarshal([]byte(r.Command), &status); err != nil {
				log.Warningf("failed to unmarshal json: %s", err)
				break
			}

			self.connection.reportStatus(status)
			self.connection.updateUser()
		}
	}
}

type sessionStatus struct {
	Type string `json:"type,omitempty"`
}

func (self *Session) gatherStatus() *sessionStatus {
	return &sessionStatus{
		Type: self.channelType,
	}
}

func (self *Session) ping() {
	defer self.Close()

	if _, err := self.channel.Write([]byte("pong\n")); err != nil {
		log.Warningf("failed to send status: %s", err)
		return
	}
}

func (self *Session) status() {
	defer self.Close()

	var status interface{}
	if !self.connection.admin {
		status = self.connection.gatherStatus()
	} else {
		status = self.connection.gateway.gatherStatus()
	}

	encoded, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Warningf("failed to marshal status: %s", err)
		return
	}

	if _, err := self.channel.Write(encoded); err != nil {
		log.Warningf("failed to send status: %s", err)
		return
	}

	if _, err := self.channel.Write([]byte("\n")); err != nil {
		log.Warningf("failed to send status: %s", err)
		return
	}
}

func (self *Session) reportStatus() {
	go func() {
		defer self.Close()

		reader, err := gzip.NewReader(self.channel)
		if err != nil {
			log.Warningf("failed to decompress: %s", err)
			return
		}
		defer reader.Close()

		// read all data from session
		raw, err := ioutil.ReadAll(reader)
		if err != nil {
			log.Warningf("failed to read all: %s", err)
			return
		}

		// parse it in to json
		var status json.RawMessage
		if err := json.Unmarshal(raw, &status); err != nil {
			log.Warningf("failed to unmarshal json: %s", err)
			return
		}

		// save the result
		self.connection.reportStatus(status)
		self.connection.updateUser()
	}()
}
