package gateway

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

var (
	ErrInvalidCommand = errors.New("gateway: invalid command")
	ErrNotFound       = errors.New("gateway: not found")
)

type service struct {
	connection *connection
	channel    ssh.Channel
	terminal   *term.Terminal
}

func runService(command string, connection *connection, channel ssh.Channel) error {
	self := &service{
		connection: connection,
		channel:    channel,
	}
	if len(command) == 0 {
		self.terminal = term.NewTerminal(channel, "")
		return self.handleShell()
	}
	if err := self.handleCommand(splitCommand(command)); err != nil {
		if err == ErrInvalidCommand {
			// legacy behavior, command itself is json
			var status json.RawMessage
			if err := json.Unmarshal([]byte(command), &status); err != nil {
				log.Errorf("%s: failed to unmarshal json: %s", self.connection, err)
				return err
			}
			return self.connection.reportStatus(status)
		}
	}
	return nil
}

// func (self *service) unmarshal(v interface{}) error {
// 	input, err := self.read("json> ")
// 	if err != nil {
// 		return err
// 	}
// 	if err := json.Unmarshal(input, v); err != nil {
// 		return err
// 	}
// 	return nil
// }

func (self *service) marshal(v interface{}) error {
	output, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return self.write(append(output, byte('\n')))
}

// func (self *service) read(prompt string) ([]byte, error) {
// 	if self.terminal != nil {
// 		self.terminal.SetPrompt(prompt)
// 		line, err := self.terminal.ReadLine()
// 		if err != nil {
// 			return nil, err
// 		}
// 		return []byte(line), nil
// 	}
// 	return ioutil.ReadAll(self.channel)
// }

func (self *service) write(data []byte) error {
	if self.terminal != nil {
		if _, err := self.terminal.Write(data); err != nil {
			return err
		}
		return nil
	}
	if _, err := self.channel.Write(data); err != nil {
		return err
	}
	return nil
}

func (self *service) handleShell() error {
	if err := self.write([]byte(fmt.Sprintf("Welcome to gatewaysshd version %s! Type \"help\" to get a list of available commands.\n", self.connection.gateway.settings.Version))); err != nil {
		return err
	}
	for {
		self.terminal.SetPrompt("gatewaysshd> ")
		line, err := self.terminal.ReadLine()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		switch line {
		case "exit", "quit":
			return nil
		}
		if err := self.handleCommand(splitCommand(line)); err != nil {
			if _, err := self.terminal.Write([]byte(fmt.Sprintf("ERROR: %s\n", err))); err != nil {
				return err
			}
			continue
		}
	}
}

func (self *service) handleCommand(command []string) error {
	switch {
	case len(command) == 1 && command[0] == "help":
		return self.help()
	case len(command) == 1 && command[0] == "ping":
		return self.ping()
	case len(command) == 1 && command[0] == "version":
		return self.version()
	case len(command) == 1 && command[0] == "reportStatus":
		return self.reportStatus()
	case len(command) == 1 && command[0] == "reportScreenshot":
		return self.reportScreenshot()
	}

	if !self.connection.permitPortForwarding {
		return ErrInvalidCommand
	}

	switch {
	case len(command) == 1 && command[0] == "status":
		return self.status("")
	case len(command) == 2 && command[0] == "status":
		return self.status(command[1])
	case len(command) == 1 && command[0] == "listUsers":
		return self.listUsers()
	case len(command) == 2 && command[0] == "getUser":
		return self.getUser(command[1])
	}

	if !self.connection.administrator {
		return ErrInvalidCommand
	}
	switch {
	case len(command) == 2 && command[0] == "kickUser":
		return self.kickUser(command[1])
	}
	return ErrInvalidCommand
}

func (self *service) help() error {
	content := []string{
		"Available commands:",
		"",
	}

	if self.connection.permitPortForwarding {
		content = append(content, []string{
			"    status [username] - show gateway status, optionally filter by username",
			"    listUsers - list all users in database",
			"    getUser <username> - get details about a user from database",
			"",
		}...)
	}

	if self.connection.administrator {
		content = append(content, []string{
			"    kickUser <username> - close connections from a user",
			"",
		}...)
	}

	content = append(content, []string{
		"    exit - exit shell",
		"    quit - same as exit",
		"    help - display this help message",
		"    version - show server version",
		"    ping - ping server",
		"",
		"",
	}...)
	return self.write([]byte(strings.Join(content, "\n")))
}

func (self *service) ping() error {
	data, err := json.Marshal(map[string]interface{}{
		"version": self.connection.gateway.settings.Version,
		"services": []string{
			"ping",
			"reportStatus",
			"reportScreenshot",
		},
		"now": time.Now().In(time.Local),
	})
	if err != nil {
		return err
	}
	if err := self.write(data); err != nil {
		return err
	}
	return self.write([]byte("\n"))
}

func (self *service) version() error {
	return self.write([]byte(fmt.Sprintf("%s\n", self.connection.gateway.settings.Version)))
}

func (self *service) status(user string) error {
	var status interface{}
	if !self.connection.permitPortForwarding {
		status = self.connection.gatherStatus()
	} else {
		status = self.connection.gateway.gatherStatus(user)
	}
	return self.marshal(status)
}

func (self *service) reportStatus() error {
	if self.terminal != nil {
		return fmt.Errorf("gateway: this command cannot be run in shell")
	}

	reader, err := gzip.NewReader(self.channel)
	if err != nil {
		log.Errorf("%s: failed to decompress: %s", self, err)
		return err
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warningf("failed to close reader: %v", err)
		}
	}()

	// read all data from session
	raw, err := io.ReadAll(reader)
	if err != nil {
		log.Errorf("%s: failed to read all: %s", self, err)
		return err
	}

	// parse it in to json
	var status json.RawMessage
	if err := json.Unmarshal(raw, &status); err != nil {
		log.Errorf("%s: failed to unmarshal json: %s", self, err)
		return err
	}

	// save the result
	return self.connection.reportStatus(status)
}

func (self *service) reportScreenshot() error {
	if self.terminal != nil {
		return fmt.Errorf("gateway: this command cannot be run in shell")
	}

	// read all data from session
	screenshot, err := io.ReadAll(self.channel)
	if err != nil {
		log.Errorf("%s: failed to read all: %s", self, err)
		return err
	}

	// save the result
	return self.connection.reportScreenshot(screenshot)
}

func (self *service) listUsers() error {
	output, err := self.connection.gateway.ListUsers()
	if err != nil {
		return err
	}
	return self.marshal(output)
}

func (self *service) getUser(userId string) error {
	output, err := self.connection.gateway.GetUser(userId)
	if err != nil {
		return err
	}
	return self.marshal(output)
}

func (self *service) kickUser(userId string) error {
	if err := self.connection.gateway.kickUser(userId); err != nil {
		return err
	}
	return nil
}
