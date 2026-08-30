// Package keychain provides a narrow macOS Keychain adapter.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	securityExecutable             = "/usr/bin/security"
	keychainItemNotFoundExitCode   = 44
	maxIdentifierBytes             = 256
	maxSecretBytes                 = 64 << 10
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
	return newGenericPasswordStore(execRunner{})
}

func newGenericPasswordStore(runner commandRunner) *genericPasswordStore {
	return &genericPasswordStore{runner: runner}
}

func (store *genericPasswordStore) Set(ctx context.Context, service, account, secret string) error {
	if err := validateCall(ctx, store, service, account); err != nil {
		return err
	}
	if len(secret) == 0 || len(secret) > maxSecretBytes || strings.IndexByte(secret, 0) >= 0 || !utf8.ValidString(secret) {
		return fmt.Errorf("invalid generic password secret")
	}
	args := []string{"add-generic-password", "-U", "-s", service, "-a", account, "-w"}
	_, _, err := store.runner.Run(ctx, securityExecutable, args, []byte(secret))
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
	stdout, _, err := store.runner.Run(ctx, securityExecutable, args, nil)
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
	_, _, err := store.runner.Run(ctx, securityExecutable, args, nil)
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
	if len(value) == 0 || len(value) > maxIdentifierBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
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

type execRunner struct{}

func (execRunner) Run(ctx context.Context, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	command.Stdin = bytes.NewReader(stdin)
	stdout := &boundedBuffer{maxBytes: maxSecurityCommandOutputBytes}
	stderr := &boundedBuffer{maxBytes: maxSecurityCommandMessageBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
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
