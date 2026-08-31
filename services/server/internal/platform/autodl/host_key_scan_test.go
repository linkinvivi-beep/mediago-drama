package autodl

import (
	"context"
	"net"
	"strconv"
	"testing"
)

func TestSSHHostKeyScannerCapturesFingerprintWithoutCredentials(t *testing.T) {
	server := newFakeSSHServer(t, "password-that-must-not-be-sent")
	host, portText, err := net.SplitHostPort(server.address())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	got, err := NewSSHHostKeyScanner().Scan(context.Background(), host, port)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if got != server.fingerprint {
		t.Fatalf("Scan() = %q, want %q", got, server.fingerprint)
	}
	if server.connectionCount.Load() != 0 {
		t.Fatalf("authenticated SSH connections = %d, want 0", server.connectionCount.Load())
	}
}

func TestSSHHostKeyScannerHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSSHHostKeyScanner().Scan(ctx, "127.0.0.1", 22)
	if err != context.Canceled {
		t.Fatalf("Scan() error = %v, want context.Canceled", err)
	}
}
