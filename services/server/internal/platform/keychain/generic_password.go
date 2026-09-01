// Package keychain provides a narrow macOS Keychain adapter.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const (
	securityExecutable           = "/usr/bin/security"
	keychainItemNotFoundExitCode = 44
	maxIdentifierBytes           = 128
	// security(1) reads -w through getpass. Keep the UTF-8 password at or
	// below 127 bytes so the terminating newline also fits its conservative
	// 128-byte input budget without truncation.
	maxSecretBytes                 = 127
	maxSecurityCommandOutputBytes  = maxSecretBytes + 2
	maxSecurityCommandMessageBytes = 4 << 10
)

var (
	// ErrNotFound means the requested generic password does not exist.
	ErrNotFound = errors.New("generic password not found")
	// ErrUnavailable means macOS Keychain could not complete the operation.
	ErrUnavailable = errors.New("generic password store unavailable")
)

// GenericPasswordStore is the password-store contract used by AutoDL
// settings. Implementations must not expose secrets in process arguments or
// errors.
type GenericPasswordStore interface {
	Set(ctx context.Context, service, account, secret string) error
	Get(ctx context.Context, service, account string) (string, error)
	Delete(ctx context.Context, service, account string) error
}

type commandRunner interface {
	Run(ctx context.Context, path string, args []string, stdin []byte) (stdout, stderr []byte, err error)
}

type genericPasswordStore struct {
	runner commandRunner
}

// NewGenericPasswordStore creates the production macOS Keychain adapter.
func NewGenericPasswordStore() GenericPasswordStore {
	return newPlatformGenericPasswordStore()
}

func newGenericPasswordStore(runner commandRunner) *genericPasswordStore {
	return &genericPasswordStore{runner: runner}
}

func (store *genericPasswordStore) Set(ctx context.Context, service, account, secret string) error {
	if err := validateCall(ctx, store, service, account); err != nil {
		return err
	}
	if !validSecret(secret) {
		return fmt.Errorf("invalid generic password secret")
	}
	args := []string{"add-generic-password", "-U", "-s", service, "-a", account, "-w"}
	stdin := make([]byte, len(secret)+1)
	copy(stdin, secret)
	stdin[len(secret)] = '\n'
	defer clear(stdin)
	stdout, stderr, err := store.runner.Run(ctx, securityExecutable, args, stdin)
	defer clear(stdout)
	defer clear(stderr)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("setting generic password: %w", ErrUnavailable)
	}
	return nil
}

func (store *genericPasswordStore) Get(ctx context.Context, service, account string) (string, error) {
	if err := validateCall(ctx, store, service, account); err != nil {
		return "", err
	}
	args := []string{"find-generic-password", "-s", service, "-a", account, "-w"}
	stdout, stderr, err := store.runner.Run(ctx, securityExecutable, args, nil)
	defer clear(stdout)
	defer clear(stderr)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		if commandExitCode(err) == keychainItemNotFoundExitCode {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("reading generic password: %w", ErrUnavailable)
	}
	return trimTerminalNewline(string(stdout)), nil
}

func (store *genericPasswordStore) Delete(ctx context.Context, service, account string) error {
	if err := validateCall(ctx, store, service, account); err != nil {
		return err
	}
	args := []string{"delete-generic-password", "-s", service, "-a", account}
	stdout, stderr, err := store.runner.Run(ctx, securityExecutable, args, nil)
	defer clear(stdout)
	defer clear(stderr)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if commandExitCode(err) == keychainItemNotFoundExitCode {
			return nil
		}
		return fmt.Errorf("deleting generic password: %w", ErrUnavailable)
	}
	return nil
}

func validateCall(ctx context.Context, store *genericPasswordStore, service, account string) error {
	if ctx == nil {
		return fmt.Errorf("generic password context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || store.runner == nil {
		return fmt.Errorf("generic password store is unavailable: %w", ErrUnavailable)
	}
	if !validIdentifier(service) || !validIdentifier(account) {
		return fmt.Errorf("invalid generic password identifier")
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > maxIdentifierBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if index == 0 {
			if !isASCIIAlphaNumeric(char) {
				return false
			}
			continue
		}
		if !isASCIIAlphaNumeric(char) && char != '.' && char != '_' && char != ':' && char != '-' {
			return false
		}
	}
	return true
}

func validSecret(secret string) bool {
	if len(secret) == 0 || len(secret) > maxSecretBytes || !utf8.ValidString(secret) {
		return false
	}
	for _, char := range secret {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func isASCIIAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

func trimTerminalNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(value, "\n")
		value = strings.TrimSuffix(value, "\r")
	}
	return value
}

type exitCoder interface {
	ExitCode() int
}

func commandExitCode(err error) int {
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

type securityProcess interface {
	ConfigureIO(stdin io.Reader, stdout, stderr io.Writer)
	DetachFromControllingTTY()
	Run() error
}

type securityCommandFactory interface {
	CommandContext(ctx context.Context, path string, args ...string) securityProcess
}

type execRunner struct {
	factory securityCommandFactory
}

func (runner execRunner) Run(ctx context.Context, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	defer clear(stdin)
	factory := runner.factory
	if factory == nil {
		factory = execSecurityCommandFactory{}
	}
	command := factory.CommandContext(ctx, path, args...)
	stdout := &boundedBuffer{maxBytes: maxSecurityCommandOutputBytes}
	stderr := &boundedBuffer{maxBytes: maxSecurityCommandMessageBytes}
	command.ConfigureIO(bytes.NewReader(stdin), stdout, stderr)
	command.DetachFromControllingTTY()
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type execSecurityCommandFactory struct{}

func (execSecurityCommandFactory) CommandContext(ctx context.Context, path string, args ...string) securityProcess {
	return &execSecurityProcess{command: exec.CommandContext(ctx, path, args...)}
}

type execSecurityProcess struct {
	command *exec.Cmd
}

func (process *execSecurityProcess) ConfigureIO(stdin io.Reader, stdout, stderr io.Writer) {
	process.command.Stdin = stdin
	process.command.Stdout = stdout
	process.command.Stderr = stderr
}

func (process *execSecurityProcess) DetachFromControllingTTY() {
	// A new session has no controlling terminal, forcing security(1)'s
	// getpass path to consume the supplied stdin pipe instead of /dev/tty.
	// Noctty is intentionally omitted: Go applies it to fd 0 after fd
	// remapping, where our pipe would make TIOCNOTTY fail with ENOTTY.
	process.command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func (process *execSecurityProcess) Run() error {
	return process.command.Run()
}

type boundedBuffer struct {
	bytes.Buffer
	maxBytes int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.maxBytes - buffer.Len()
	if remaining <= 0 {
		return 0, fmt.Errorf("security command output is too large")
	}
	if len(data) > remaining {
		written, _ := buffer.Buffer.Write(data[:remaining])
		return written, fmt.Errorf("security command output is too large")
	}
	return buffer.Buffer.Write(data)
}
