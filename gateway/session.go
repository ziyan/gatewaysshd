package gateway

import (
	"fmt"

	"golang.org/x/crypto/ssh"
)

// handle session within a ssh connection
func handleSession(connection *connection, channel ssh.Channel, requests <-chan *ssh.Request, channelType string, extraData []byte) {
	log.Debugf("%s: new session: type = %s", connection, channelType)

	// hanlde requests
	for request := range requests {
		// log.Debugf("%s: request received: type = %s, want_reply = %v, payload = %d", connection, request.Type, request.WantReply, len(request.Payload))

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
				log.Errorf("%s: failed to reply to request: %s", connection, err)
				break
			}
		}

		switch request.Type {
		case "shell":
		case "exec":
		default:
			continue
		}

		if err := func() error {
			success := false
			defer func() {
				if err := channel.CloseWrite(); err != nil {
					log.Warningf("%s: failed to close session: %s", connection, err)
				}

				exitStatus := struct{ Status uint32 }{}
				if !success {
					exitStatus.Status = 1
				}
				if _, err := channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus)); err != nil {
					log.Warningf("%s: failed to send exit-status for session: %s", connection, err)
				}

				if err := channel.Close(); err != nil {
					log.Warningf("%s: failed to close session: %s", connection, err)
				}
			}()

			// decode command
			var command string
			if request.Type == "exec" {
				var execute struct{ Command string }
				if err := ssh.Unmarshal(request.Payload, &execute); err != nil {
					log.Errorf("%s: failed to unmarshal request payload: %s: %v", connection, err, request.Payload)
					return err
				}
				command = execute.Command
			}

			// do actual work here
			if err := runService(command, connection, channel); err != nil {
				log.Errorf("%s: command %s failed: %s", connection, command, err)
				if _, err2 := fmt.Fprintf(channel.Stderr(), "ERROR: %s\r\n", err); err2 != nil {
					log.Errorf("%s: failed to write to stdout: %s", connection, err2)
				}
				return err
			}

			success = true
			return nil
		}(); err != nil {
			break
		}
	}
}
