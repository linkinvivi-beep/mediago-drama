package autodl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

const hostKeyScanTimeout = 10 * time.Second

var errHostKeyCaptured = errors.New("AutoDL SSH host key captured")

// HostKeyScanner performs only the SSH key exchange required to observe a
// server key. It does not authenticate and does not open a remote channel.
type HostKeyScanner interface {
	Scan(context.Context, string, int) (string, error)
}

// SSHHostKeyScanner is the production bounded SSH key scanner.
type SSHHostKeyScanner struct{}

func NewSSHHostKeyScanner() SSHHostKeyScanner { return SSHHostKeyScanner{} }

func (SSHHostKeyScanner) Scan(ctx context.Context, host string, port int) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("AutoDL host key scan context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if host == "" || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid AutoDL SSH scan target")
	}

	scanCtx, cancel := context.WithTimeout(ctx, hostKeyScanTimeout)
	defer cancel()
	address := net.JoinHostPort(host, strconv.Itoa(port))
	connection, err := (&net.Dialer{}).DialContext(scanCtx, "tcp", address)
	if err != nil {
		if contextErr := scanCtx.Err(); contextErr != nil {
			return "", contextErr
		}
		return "", fmt.Errorf("connect AutoDL SSH for host key scan: %w", err)
	}
	defer connection.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-scanCtx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	defer close(done)

	var fingerprint string
	config := &ssh.ClientConfig{
		User: "medialink-host-key-scan",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return errHostKeyCaptured
		},
	}
	_, _, _, handshakeErr := ssh.NewClientConn(connection, address, config)
	if contextErr := scanCtx.Err(); contextErr != nil {
		return "", contextErr
	}
	if fingerprint == "" || !errors.Is(handshakeErr, errHostKeyCaptured) {
		return "", fmt.Errorf("read AutoDL SSH host key: %w", handshakeErr)
	}
	return fingerprint, nil
}
