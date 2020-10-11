package gateway

import (
	"fmt"

	"golang.org/x/crypto/ssh"
)

// handle session within a ssh connection
func handleSession(connection *connection, channel ssh.Channel, requests <-chan *ssh.Request, channelType string, extraData []byte) {
	log.Debugf("new session: user = %s, remote = %v, type = %s", connection.user, connection.remoteAddr, channelType)

	// hanlde requests
	for request := range requests {
		log.Debugf("request received: type = %s, want_reply = %v, payload = %v", request.Type, request.WantReply, request.Payload)

		// check parameters
		ok := true
		switch request.Type {
		case "env":
			// just ignore the env settings from client
		case "shell":
			// allow creating shell
		case "exec":
			// allow execute command
		case "pty-req":
			// allow pty
		default:
			ok = false
		}

		// reply to client
		if request.WantReply {
			if err := request.Reply(ok, nil); err != nil {
				log.Warningf("failed to reply to request: %s", err)
				continue
			}
		}

		switch request.Type {
		case "shell":
		case "exec":
		default:
			continue
		}

		func() {
			success := false
			defer func() {
				if err := channel.CloseWrite(); err != nil {
					log.Warningf("failed to close session: %s", err)
				}

				exitStatus := struct{ Status uint32 }{}
				if !success {
					exitStatus.Status = 1
				}
				if _, err := channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus)); err != nil {
					log.Warningf("failed to send exit-status for session: %s", err)
				}

				if err := channel.Close(); err != nil {
					log.Warningf("failed to close session: %s", err)
				}
			}()

			// decode command
			var command string
			if request.Type == "exec" {
				var execute struct{ Command string }
				if err := ssh.Unmarshal(request.Payload, &execute); err != nil {
					log.Warningf("failed to unmarshal request payload: %s: %v", err, request.Payload)
					return
				}
				command = execute.Command
			}

			// do actual work here
			if err := runService(command, connection, channel); err != nil {
				log.Warningf("command %s failed: %s", command, err)
				if _, err := fmt.Fprintf(channel.Stderr(), "ERROR: %s\r\n", err); err != nil {
					log.Warningf("failed to write to stdout: %s", err)
				}
				return
			}

			success = true
		}()
	}
}
