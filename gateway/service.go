package gateway

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

var (
	ErrInvalidCommand = errors.New("gateway: invalid command")
	ErrNotFound       = errors.New("gateway: not found")
)

type service struct {
	connection *connection
	channel    ssh.Channel
	terminal   *terminal.Terminal
}

func runService(command string, connection *connection, channel ssh.Channel) error {
	self := &service{
		connection: connection,
		channel:    channel,
	}
	if len(command) == 0 {
		self.terminal = terminal.NewTerminal(channel, "")
		return self.handleShell()
	}
	if err := self.handleCommand(splitCommand(command)); err != nil {
		if err == ErrInvalidCommand {
			// legacy behavior, command itself is json
			var status json.RawMessage
			if err := json.Unmarshal([]byte(command), &status); err != nil {
				log.Errorf("failed to unmarshal json: %s", err)
				return err
			}
			return self.connection.reportStatus(status)
		}
	}
	return nil
}

func (self *service) unmarshal(v interface{}) error {
	input, err := self.read("json> ")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(input, v); err != nil {
		return err
	}
	return nil
}

func (self *service) marshal(v interface{}) error {
	output, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return self.write(append(output, byte('\n')))
}

func (self *service) read(prompt string) ([]byte, error) {
	if self.terminal != nil {
		self.terminal.SetPrompt(prompt)
		line, err := self.terminal.ReadLine()
		if err != nil {
			return nil, err
		}
		return []byte(line), nil
	}
	return ioutil.ReadAll(self.channel)
}

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
	self.write([]byte(fmt.Sprintf("Welcome to gatewaysshd version %s! Type \"help\" to get a list of available commands.\n", self.connection.gateway.settings.Version)))
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
			if _, err2 := self.terminal.Write([]byte(fmt.Sprintf("ERROR: %s\n", err))); err2 != nil {
				return err2
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
	}

	if !self.connection.permitPortForwarding {
		return ErrInvalidCommand
	}

	switch {
	case len(command) == 1 && command[0] == "status":
		return self.status("")
	case len(command) == 2 && command[0] == "status":
		return self.status(command[1])
	}

	if !self.connection.administrator {
		return ErrInvalidCommand
	}
	switch {
	case len(command) == 1 && command[0] == "listUsers":
		return self.listUsers()
	case len(command) == 2 && command[0] == "getUser":
		return self.getUser(command[1])
	}
	return ErrInvalidCommand
}

func (self *service) help() error {
	content := []string{
		fmt.Sprintf("Available commands:"),
		"",
		fmt.Sprintf("    ping - ping server"),
		"",
	}

	if self.connection.permitPortForwarding {
		content = append(content, []string{
			fmt.Sprintf("    status [username] - show gateway status, optionally filter by username"),
			"",
		}...)
	}

	if self.connection.administrator {
		content = append(content, []string{
			fmt.Sprintf("    listUsers - list all users"),
			fmt.Sprintf("    getUser <username> - get details about a user"),
			"",
		}...)
	}

	content = append(content, []string{
		fmt.Sprintf("    exit - exit shell"),
		fmt.Sprintf("    quit - same as exit"),
		fmt.Sprintf("    help - display this help message"),
		"",
		"",
	}...)
	return self.write([]byte(strings.Join(content, "\n")))
}

func (self *service) ping() error {
	return self.write([]byte("pong\n"))
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
		log.Errorf("failed to decompress: %s", err)
		return err
	}
	defer reader.Close()

	// read all data from session
	raw, err := ioutil.ReadAll(reader)
	if err != nil {
		log.Errorf("failed to read all: %s", err)
		return err
	}

	// parse it in to json
	var status json.RawMessage
	if err := json.Unmarshal(raw, &status); err != nil {
		log.Errorf("failed to unmarshal json: %s", err)
		return err
	}

	// save the result
	return self.connection.reportStatus(status)
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
