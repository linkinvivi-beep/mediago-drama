// Package autodl contains data-only adapters for AutoDL connections.
package autodl

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	defaultSSHPort  = 22
	maxCommandBytes = 4096
	maxUserBytes    = 64
)

// SSHLoginTarget is the connection target extracted from a restricted SSH
// login command. Parsing a target never executes the supplied command.
type SSHLoginTarget struct {
	Host string
	Port int
	User string
}

// ParseSSHLoginCommand parses the small, non-executable subset of OpenSSH
// syntax accepted by MediaLink: ssh, optional -p/-l options, and one target.
func ParseSSHLoginCommand(input string) (SSHLoginTarget, error) {
	if len(input) == 0 || len(input) > maxCommandBytes {
		return SSHLoginTarget{}, invalidSSHCommand()
	}
	for _, char := range input {
		if char < 0x20 || char > 0x7e || char == 0x7f {
			return SSHLoginTarget{}, invalidSSHCommand()
		}
		switch char {
		case '\'', '"', '\\', '`', '$', ';', '|', '&', '<', '>', '(', ')', '{', '}', '[', ']', '*', '?', '!', '#', '~':
			return SSHLoginTarget{}, invalidSSHCommand()
		}
	}

	fields := strings.Fields(input)
	if len(fields) < 2 || fields[0] != "ssh" {
		return SSHLoginTarget{}, invalidSSHCommand()
	}

	target := SSHLoginTarget{Port: defaultSSHPort}
	portSet := false
	userSet := false
	position := 1
	for position < len(fields) && strings.HasPrefix(fields[position], "-") {
		option := fields[position]
		position++
		if position >= len(fields) {
			return SSHLoginTarget{}, invalidSSHCommand()
		}
		value := fields[position]
		position++

		switch option {
		case "-p":
			if portSet {
				return SSHLoginTarget{}, invalidSSHCommand()
			}
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return SSHLoginTarget{}, invalidSSHCommand()
			}
			target.Port = port
			portSet = true
		case "-l":
			if userSet || !validSSHUser(value) {
				return SSHLoginTarget{}, invalidSSHCommand()
			}
			target.User = value
			userSet = true
		default:
			return SSHLoginTarget{}, invalidSSHCommand()
		}
	}

	if position != len(fields)-1 {
		return SSHLoginTarget{}, invalidSSHCommand()
	}
	destination := fields[position]
	if strings.Count(destination, "@") > 1 {
		return SSHLoginTarget{}, invalidSSHCommand()
	}
	if separator := strings.IndexByte(destination, '@'); separator >= 0 {
		if userSet {
			return SSHLoginTarget{}, invalidSSHCommand()
		}
		target.User = destination[:separator]
		target.Host = destination[separator+1:]
		if !validSSHUser(target.User) {
			return SSHLoginTarget{}, invalidSSHCommand()
		}
	} else {
		target.Host = destination
	}
	if !validSSHHost(target.Host) {
		return SSHLoginTarget{}, invalidSSHCommand()
	}
	return target, nil
}

func validSSHUser(user string) bool {
	if len(user) == 0 || len(user) > maxUserBytes {
		return false
	}
	for index := 0; index < len(user); index++ {
		char := user[index]
		if index == 0 {
			if !isASCIILetter(char) && char != '_' {
				return false
			}
			continue
		}
		if !isASCIILetter(char) && !isASCIIDigit(char) && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func validSSHHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4() != nil
	}
	allNumericOrDots := true
	for index := 0; index < len(host); index++ {
		if !isASCIIDigit(host[index]) && host[index] != '.' {
			allNumericOrDots = false
			break
		}
	}
	if allNumericOrDots {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := 0; index < len(label); index++ {
			char := label[index]
			if !isASCIILetter(char) && !isASCIIDigit(char) && char != '-' {
				return false
			}
		}
	}
	return true
}

func isASCIILetter(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func isASCIIDigit(char byte) bool {
	return char >= '0' && char <= '9'
}

func invalidSSHCommand() error {
	return fmt.Errorf("invalid SSH login command")
}
