package autodl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestTunnelManagerOpensIndependentLoopbackTunnels(t *testing.T) {
	firstServer := newFakeSSHServer(t, "first-password")
	secondServer := newFakeSSHServer(t, "second-password")
	passwords := &fakeTunnelPasswordSource{values: map[string][]byte{
		"credential-first":  []byte("first-password"),
		"credential-second": []byte("second-password"),
	}}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })

	firstTarget := fakeTunnelTarget("instance-first", "credential-first", firstServer, 6006)
	secondTarget := fakeTunnelTarget("instance-second", "credential-second", secondServer, 8188)
	first, err := manager.Ensure(context.Background(), firstTarget)
	if err != nil {
		t.Fatalf("Ensure(first) error = %v", err)
	}
	second, err := manager.Ensure(context.Background(), secondTarget)
	if err != nil {
		t.Fatalf("Ensure(second) error = %v", err)
	}

	if first.InstanceProfileID != "instance-first" || second.InstanceProfileID != "instance-second" {
		t.Fatalf("tunnel identities = %q, %q", first.InstanceProfileID, second.InstanceProfileID)
	}
	firstAddress := tunnelAddress(t, first)
	secondAddress := tunnelAddress(t, second)
	if firstAddress == secondAddress {
		t.Fatalf("two instances shared local address %q", firstAddress)
	}
	assertLoopbackAddress(t, firstAddress)
	assertLoopbackAddress(t, secondAddress)
	assertTunnelEcho(t, firstAddress, "first")
	assertTunnelEcho(t, secondAddress, "second")
	if got := receiveRemoteAddress(t, firstServer.remoteAddresses); got != "127.0.0.1:6006" {
		t.Fatalf("first remote address = %q, want 127.0.0.1:6006", got)
	}
	if got := receiveRemoteAddress(t, secondServer.remoteAddresses); got != "127.0.0.1:8188" {
		t.Fatalf("second remote address = %q, want 127.0.0.1:8188", got)
	}
}

func TestTunnelManagerRejectsFingerprintMismatchAndRetiresChangedTarget(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	passwords := &fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	target := fakeTunnelTarget("instance", "credential", server, 6006)
	first, err := manager.Ensure(context.Background(), target)
	if err != nil {
		t.Fatalf("Ensure(valid) error = %v", err)
	}
	oldAddress := tunnelAddress(t, first)

	target.HostFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := manager.Ensure(context.Background(), target); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("Ensure(mismatch) error = %v, want ErrHostKeyMismatch", err)
	}
	assertEventuallyDialFails(t, oldAddress)
}

func TestTunnelManagerDeduplicatesConcurrentEnsure(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	passwords := &fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	target := fakeTunnelTarget("instance", "credential", server, 6006)

	const callers = 32
	start := make(chan struct{})
	results := make(chan Tunnel, callers)
	errorsChannel := make(chan error, callers)
	var workers sync.WaitGroup
	for index := 0; index < callers; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			tunnel, err := manager.Ensure(context.Background(), target)
			results <- tunnel
			errorsChannel <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent Ensure error = %v", err)
		}
	}
	var baseURL string
	for result := range results {
		if baseURL == "" {
			baseURL = result.BaseURL
		}
		if result.BaseURL != baseURL {
			t.Fatalf("concurrent Ensure BaseURL = %q, want %q", result.BaseURL, baseURL)
		}
	}
	if got := server.connectionCount.Load(); got != 1 {
		t.Fatalf("SSH connection count = %d, want 1", got)
	}
}

func TestTunnelManagerLatestDifferentTargetWins(t *testing.T) {
	firstServer := newFakeSSHServer(t, "first-password")
	secondServer := newFakeSSHServer(t, "second-password")
	passwords := &supersedingTunnelPasswordSource{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		values: map[string][]byte{
			"credential-first":  []byte("first-password"),
			"credential-second": []byte("second-password"),
		},
	}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	firstTarget := fakeTunnelTarget("instance", "credential-first", firstServer, 6006)
	secondTarget := fakeTunnelTarget("instance", "credential-second", secondServer, 8188)
	firstResult := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(context.Background(), firstTarget)
		firstResult <- err
	}()
	<-passwords.firstStarted

	secondResult := make(chan struct {
		tunnel Tunnel
		err    error
	}, 1)
	go func() {
		tunnel, err := manager.Ensure(context.Background(), secondTarget)
		secondResult <- struct {
			tunnel Tunnel
			err    error
		}{tunnel: tunnel, err: err}
	}()
	var second Tunnel
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatalf("Ensure(new target) error = %v", result.err)
		}
		second = result.tunnel
	case <-time.After(time.Second):
		t.Fatal("new target waited for superseded credential lookup")
	}
	close(passwords.releaseFirst)
	select {
	case err := <-firstResult:
		if !errors.Is(err, ErrTunnelSuperseded) {
			t.Fatalf("Ensure(old target) error = %v, want ErrTunnelSuperseded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("superseded target did not finish")
	}
	reused, err := manager.Ensure(context.Background(), secondTarget)
	if err != nil {
		t.Fatalf("Ensure(latest target again) error = %v", err)
	}
	if reused.BaseURL != second.BaseURL {
		t.Fatalf("latest target BaseURL = %q, want %q", reused.BaseURL, second.BaseURL)
	}
	if firstServer.connectionCount.Load() > 1 || secondServer.connectionCount.Load() != 1 {
		t.Fatalf("bounded SSH counts = %d, %d; want <=1, 1", firstServer.connectionCount.Load(), secondServer.connectionCount.Load())
	}
}

func TestTunnelManagerCloseInvalidatesEveryPreexistingEnsure(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	passwords := &closeEpochPasswordSource{
		firstStarted:   make(chan struct{}),
		firstCancelled: make(chan struct{}),
		releaseFirst:   make(chan struct{}),
	}
	manager := NewTunnelManager(passwords)
	target := fakeTunnelTarget("instance", "credential", server, 6006)
	firstResult := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(context.Background(), target)
		firstResult <- err
	}()
	<-passwords.firstStarted
	closeResult := make(chan error, 1)
	go func() { closeResult <- manager.Close("instance") }()
	<-passwords.firstCancelled

	if _, err := manager.Ensure(context.Background(), target); !errors.Is(err, ErrTunnelStale) {
		t.Fatalf("Ensure during Close error = %v, want ErrTunnelStale", err)
	}
	close(passwords.releaseFirst)
	select {
	case err := <-firstResult:
		if !errors.Is(err, ErrTunnelStale) {
			t.Fatalf("pre-Close Ensure error = %v, want ErrTunnelStale", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-Close Ensure did not finish")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for preexisting Ensure")
	}

	reopened, err := manager.Ensure(context.Background(), target)
	if err != nil {
		t.Fatalf("Ensure after Close error = %v", err)
	}
	assertTunnelEcho(t, tunnelAddress(t, reopened), "after-close")
	if err := manager.CloseAll(); err != nil {
		t.Fatalf("CloseAll() error = %v", err)
	}
}

func TestTunnelManagerReconnectsAfterSSHDisconnect(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	t.Cleanup(func() { _ = manager.CloseAll() })
	target := fakeTunnelTarget("instance", "credential", server, 6006)
	first, err := manager.Ensure(context.Background(), target)
	if err != nil {
		t.Fatalf("Ensure(first) error = %v", err)
	}
	firstAddress := tunnelAddress(t, first)
	server.closeConnections()
	assertEventuallyDialFails(t, firstAddress)

	second, err := manager.Ensure(context.Background(), target)
	if err != nil {
		t.Fatalf("Ensure(reconnect) error = %v", err)
	}
	secondAddress := tunnelAddress(t, second)
	if secondAddress == firstAddress {
		t.Fatalf("reconnect reused closed listener %q", firstAddress)
	}
	assertTunnelEcho(t, secondAddress, "reconnected")
	if got := server.connectionCount.Load(); got != 2 {
		t.Fatalf("SSH connection count = %d, want 2", got)
	}
}

func TestTunnelManagerReplacesSameInstanceWhenRemotePortChanges(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	t.Cleanup(func() { _ = manager.CloseAll() })
	target := fakeTunnelTarget("instance", "credential", server, 6006)
	first, err := manager.Ensure(context.Background(), target)
	if err != nil {
		t.Fatalf("Ensure(first) error = %v", err)
	}
	firstAddress := tunnelAddress(t, first)
	target.ComfyPort = 8188
	second, err := manager.Ensure(context.Background(), target)
	if err != nil {
		t.Fatalf("Ensure(changed port) error = %v", err)
	}
	assertEventuallyDialFails(t, firstAddress)
	assertTunnelEcho(t, tunnelAddress(t, second), "changed")
	if got := receiveRemoteAddress(t, server.remoteAddresses); got != "127.0.0.1:8188" {
		t.Fatalf("replacement remote address = %q, want 127.0.0.1:8188", got)
	}
}

func TestTunnelManagerReplacesSameInstanceWhenSSHServerChanges(t *testing.T) {
	firstServer := newFakeSSHServer(t, "password")
	secondServer := newFakeSSHServer(t, "password")
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	t.Cleanup(func() { _ = manager.CloseAll() })
	firstTarget := fakeTunnelTarget("instance", "credential", firstServer, 6006)
	first, err := manager.Ensure(context.Background(), firstTarget)
	if err != nil {
		t.Fatalf("Ensure(first server) error = %v", err)
	}
	secondTarget := fakeTunnelTarget("instance", "credential", secondServer, 6006)
	second, err := manager.Ensure(context.Background(), secondTarget)
	if err != nil {
		t.Fatalf("Ensure(second server) error = %v", err)
	}
	assertEventuallyDialFails(t, tunnelAddress(t, first))
	assertTunnelEcho(t, tunnelAddress(t, second), "new-server")
	if firstServer.connectionCount.Load() != 1 || secondServer.connectionCount.Load() != 1 {
		t.Fatalf("SSH connection counts = %d, %d; want 1, 1", firstServer.connectionCount.Load(), secondServer.connectionCount.Load())
	}
}

func TestTunnelManagerRejectsUnsafeIdentifiersBeforeCredentialLookup(t *testing.T) {
	valid := TunnelTarget{
		InstanceProfileID: "instance", Host: "gpu.example.com", SSHPort: 22, SSHUser: "root",
		ComfyPort: 6006, HostFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", CredentialRef: "credential",
	}
	tests := []struct {
		name   string
		mutate func(*TunnelTarget)
	}{
		{name: "instance control byte", mutate: func(target *TunnelTarget) { target.InstanceProfileID = "instance\x00other" }},
		{name: "credential control byte", mutate: func(target *TunnelTarget) { target.CredentialRef = "credential\nother" }},
		{name: "malformed fingerprint", mutate: func(target *TunnelTarget) { target.HostFingerprint = "SHA256:not-a-digest" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := valid
			test.mutate(&target)
			passwords := &fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}}
			manager := NewTunnelManager(passwords)
			t.Cleanup(func() { _ = manager.CloseAll() })
			if _, err := manager.Ensure(context.Background(), target); err == nil {
				t.Fatal("Ensure accepted unsafe target")
			}
			passwords.mu.Lock()
			calls := passwords.calls
			passwords.mu.Unlock()
			if calls != 0 {
				t.Fatalf("credential lookup calls = %d, want 0", calls)
			}
		})
	}
}

func TestTunnelManagerHonorsContextCancellation(t *testing.T) {
	passwords := &blockingTunnelPasswordSource{started: make(chan struct{})}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	target := TunnelTarget{
		InstanceProfileID: "instance", Host: "gpu.example.com", SSHPort: 22, SSHUser: "root",
		ComfyPort: 6006, HostFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", CredentialRef: "credential",
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(ctx, target)
		result <- err
	}()
	<-passwords.started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ensure cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ensure did not return after context cancellation")
	}
}

func TestTunnelManagerCancelsBlockedSSHHandshake(t *testing.T) {
	authenticationStarted := make(chan struct{})
	releaseAuthentication := make(chan struct{})
	var startedOnce sync.Once
	server := newFakeSSHServerWithPasswordCallback(t, func(metadata ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
		startedOnce.Do(func() { close(authenticationStarted) })
		<-releaseAuthentication
		if metadata.User() != "root" || string(supplied) != "password" {
			return nil, fmt.Errorf("authentication rejected")
		}
		return nil, nil
	})
	defer close(releaseAuthentication)
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	t.Cleanup(func() { _ = manager.CloseAll() })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(ctx, fakeTunnelTarget("instance", "credential", server, 6006))
		result <- err
	}()
	<-authenticationStarted
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked handshake error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked SSH handshake ignored context cancellation")
	}
}

func TestTunnelManagerJoinsHandshakeWatcherBeforePublishing(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	watcherStarted := make(chan struct{})
	releaseWatcher := make(chan struct{})
	handshakeReturned := make(chan struct{})
	manager := newTunnelManagerWithHooks(
		&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}},
		tunnelManagerHooks{
			beforeHandshakeWatch: func() {
				close(watcherStarted)
				<-releaseWatcher
			},
			afterHandshake: func() { close(handshakeReturned) },
		},
	)
	t.Cleanup(func() { _ = manager.CloseAll() })
	result := make(chan struct {
		tunnel Tunnel
		err    error
	}, 1)
	go func() {
		tunnel, err := manager.Ensure(context.Background(), fakeTunnelTarget("instance", "credential", server, 6006))
		result <- struct {
			tunnel Tunnel
			err    error
		}{tunnel: tunnel, err: err}
	}()
	<-watcherStarted
	<-handshakeReturned
	select {
	case got := <-result:
		t.Fatalf("Ensure returned before handshake watcher joined: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseWatcher)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Ensure() error = %v", got.err)
		}
		// finishTunnelOperation cancels its operation context immediately after
		// publishing. The joined watcher must not close the live SSH transport.
		assertTunnelEcho(t, tunnelAddress(t, got.tunnel), "watcher-joined")
	case <-time.After(time.Second):
		t.Fatal("Ensure did not finish after watcher release")
	}
}

func TestTunnelManagerSuccessfulHandshakeRemainsStableAfterOperationCancel(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	passwords := &fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	for index := 0; index < 32; index++ {
		instanceID := fmt.Sprintf("instance-%02d", index)
		tunnel, err := manager.Ensure(context.Background(), fakeTunnelTarget(instanceID, "credential", server, 6006))
		if err != nil {
			t.Fatalf("Ensure(%d) error = %v", index, err)
		}
		assertTunnelEcho(t, tunnelAddress(t, tunnel), fmt.Sprintf("stable-%02d", index))
	}
}

func TestTunnelManagerCloseAllStopsListenersAndInFlightEnsure(t *testing.T) {
	passwords := &delayedCancellationPasswordSource{started: make(chan struct{}), cancelled: make(chan struct{}), allowReturn: make(chan struct{})}
	manager := NewTunnelManager(passwords)
	inFlightResult := make(chan error, 1)
	target := TunnelTarget{
		InstanceProfileID: "instance", Host: "gpu.example.com", SSHPort: 22, SSHUser: "root",
		ComfyPort: 6006, HostFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", CredentialRef: "credential",
	}
	go func() {
		_, ensureErr := manager.Ensure(context.Background(), target)
		inFlightResult <- ensureErr
	}()
	<-passwords.started
	closeResult := make(chan error, 1)
	go func() { closeResult <- manager.CloseAll() }()
	<-passwords.cancelled
	secondCloseResult := make(chan error, 1)
	go func() { secondCloseResult <- manager.CloseAll() }()
	select {
	case err := <-closeResult:
		t.Fatalf("CloseAll returned before in-flight Ensure released: %v", err)
	default:
	}
	select {
	case err := <-secondCloseResult:
		t.Fatalf("concurrent CloseAll returned before shutdown completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(passwords.allowReturn)
	select {
	case err := <-inFlightResult:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrTunnelManagerClosed) {
			t.Fatalf("in-flight Ensure error = %v, want cancellation or closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight Ensure did not stop after CloseAll")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("CloseAll() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseAll did not wait for in-flight Ensure")
	}
	select {
	case err := <-secondCloseResult:
		if err != nil {
			t.Fatalf("concurrent CloseAll() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent CloseAll did not finish with shutdown")
	}
	if _, err := manager.Ensure(context.Background(), target); !errors.Is(err, ErrTunnelManagerClosed) {
		t.Fatalf("Ensure after CloseAll error = %v, want ErrTunnelManagerClosed", err)
	}
}

func TestTunnelManagerCloseAllStopsEveryListener(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"first": []byte("password"), "second": []byte("password")}})
	first, err := manager.Ensure(context.Background(), fakeTunnelTarget("first", "first", server, 6006))
	if err != nil {
		t.Fatalf("Ensure(first) error = %v", err)
	}
	second, err := manager.Ensure(context.Background(), fakeTunnelTarget("second", "second", server, 8188))
	if err != nil {
		t.Fatalf("Ensure(second) error = %v", err)
	}
	if err := manager.CloseAll(); err != nil {
		t.Fatalf("CloseAll() error = %v", err)
	}
	assertEventuallyDialFails(t, tunnelAddress(t, first))
	assertEventuallyDialFails(t, tunnelAddress(t, second))
}

func TestTunnelManagerClearsOwnedPasswordBytes(t *testing.T) {
	server := newFakeSSHServer(t, "password")
	passwords := &recordingTunnelPasswordSource{password: []byte("password")}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	if _, err := manager.Ensure(context.Background(), fakeTunnelTarget("instance", "credential", server, 6006)); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	for index, value := range passwords.returned {
		if value != 0 {
			t.Fatalf("owned password byte %d = %d, want zero", index, value)
		}
	}
}

func TestTunnelManagerClearsPasswordBytesReturnedWithCredentialError(t *testing.T) {
	passwords := &errorTunnelPasswordSource{returned: []byte("should-be-cleared")}
	manager := NewTunnelManager(passwords)
	t.Cleanup(func() { _ = manager.CloseAll() })
	target := TunnelTarget{
		InstanceProfileID: "instance", Host: "gpu.example.com", SSHPort: 22, SSHUser: "root",
		ComfyPort: 6006, HostFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", CredentialRef: "credential",
	}
	if _, err := manager.Ensure(context.Background(), target); err == nil {
		t.Fatal("Ensure() error = nil, want credential error")
	}
	for index, value := range passwords.returned {
		if value != 0 {
			t.Fatalf("error password byte %d = %d, want zero", index, value)
		}
	}
}

func TestTunnelForwardingDrainsRemoteResponseAfterClientCloseWrite(t *testing.T) {
	requestReceived := make(chan string, 1)
	releaseResponse := make(chan struct{})
	writeResult := make(chan error, 1)
	server := newFakeSSHServerWithChannelHandler(t, "password", func(channel ssh.Channel) {
		defer channel.Close()
		request, err := io.ReadAll(channel)
		if err != nil {
			writeResult <- err
			return
		}
		requestReceived <- string(request)
		<-releaseResponse
		_, err = io.WriteString(channel, "response:"+string(request))
		if closeErr := channel.CloseWrite(); err == nil {
			err = closeErr
		}
		writeResult <- err
	})
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	t.Cleanup(func() { _ = manager.CloseAll() })
	tunnel, err := manager.Ensure(context.Background(), fakeTunnelTarget("instance", "credential", server, 6006))
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	connection := dialTunnelTCP(t, tunnel)
	defer connection.Close()
	if _, err := io.WriteString(connection, "request-body"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatalf("client CloseWrite: %v", err)
	}
	if got := <-requestReceived; got != "request-body" {
		t.Fatalf("remote request = %q, want request-body", got)
	}
	close(releaseResponse)
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read drained response: %v", err)
	}
	if got := string(response); got != "response:request-body" {
		t.Fatalf("drained response = %q, want response:request-body", got)
	}
	if err := <-writeResult; err != nil {
		t.Fatalf("remote response write error = %v", err)
	}
}

func TestTunnelForwardingAllowsClientWriteAfterRemoteCloseWrite(t *testing.T) {
	requestReceived := make(chan string, 1)
	server := newFakeSSHServerWithChannelHandler(t, "password", func(channel ssh.Channel) {
		defer channel.Close()
		_, _ = io.WriteString(channel, "ready")
		_ = channel.CloseWrite()
		request, _ := io.ReadAll(channel)
		requestReceived <- string(request)
	})
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	t.Cleanup(func() { _ = manager.CloseAll() })
	tunnel, err := manager.Ensure(context.Background(), fakeTunnelTarget("instance", "credential", server, 6006))
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	connection := dialTunnelTCP(t, tunnel)
	defer connection.Close()
	ready, err := io.ReadAll(connection)
	if err != nil || string(ready) != "ready" {
		t.Fatalf("remote preface = %q, err = %v; want ready", string(ready), err)
	}
	if _, err := io.WriteString(connection, "after-ready"); err != nil {
		t.Fatalf("write after remote CloseWrite: %v", err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatalf("client CloseWrite: %v", err)
	}
	select {
	case got := <-requestReceived:
		if got != "after-ready" {
			t.Fatalf("remote drained request = %q, want after-ready", got)
		}
	case <-time.After(time.Second):
		t.Fatal("remote did not drain request after its CloseWrite")
	}
}

func TestTunnelForwardingCloseAllStopsHalfOpenStream(t *testing.T) {
	requestStarted := make(chan struct{})
	server := newFakeSSHServerWithChannelHandler(t, "password", func(channel ssh.Channel) {
		defer channel.Close()
		close(requestStarted)
		_, _ = io.Copy(io.Discard, channel)
	})
	manager := NewTunnelManager(&fakeTunnelPasswordSource{values: map[string][]byte{"credential": []byte("password")}})
	tunnel, err := manager.Ensure(context.Background(), fakeTunnelTarget("instance", "credential", server, 6006))
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	connection := dialTunnelTCP(t, tunnel)
	defer connection.Close()
	if _, err := io.WriteString(connection, "still-open"); err != nil {
		t.Fatalf("write half-open stream: %v", err)
	}
	<-requestStarted
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseAll() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseAll() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseAll leaked a half-open forwarding stream")
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("half-open client remained readable after CloseAll")
	}
}

func fakeTunnelTarget(instanceID, credentialRef string, server *fakeSSHServer, comfyPort int) TunnelTarget {
	host, portText, err := net.SplitHostPort(server.address())
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	return TunnelTarget{
		InstanceProfileID: instanceID,
		Host:              host,
		SSHPort:           port,
		SSHUser:           "root",
		ComfyPort:         comfyPort,
		HostFingerprint:   server.fingerprint,
		CredentialRef:     credentialRef,
	}
}

func tunnelAddress(t *testing.T, tunnel Tunnel) string {
	t.Helper()
	parsed, err := url.Parse(tunnel.BaseURL)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", tunnel.BaseURL, err)
	}
	if parsed.Scheme != "http" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatalf("BaseURL = %q, want a bare HTTP origin", tunnel.BaseURL)
	}
	return parsed.Host
}

func assertLoopbackAddress(t *testing.T, address string) {
	t.Helper()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", address, err)
	}
	if host != "127.0.0.1" || port == "" || port == "0" {
		t.Fatalf("local address = %q, want 127.0.0.1 with an assigned port", address)
	}
}

func assertTunnelEcho(t *testing.T, address, message string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial tunnel %q: %v", address, err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, message); err != nil {
		t.Fatalf("write tunnel: %v", err)
	}
	buffer := make([]byte, len(message))
	if _, err := io.ReadFull(connection, buffer); err != nil {
		t.Fatalf("read tunnel: %v", err)
	}
	if got := string(buffer); got != message {
		t.Fatalf("echo = %q, want %q", got, message)
	}
}

func dialTunnelTCP(t *testing.T, tunnel Tunnel) *net.TCPConn {
	t.Helper()
	connection, err := net.DialTimeout("tcp", tunnelAddress(t, tunnel), time.Second)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	tcpConnection, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		t.Fatalf("tunnel connection type = %T, want *net.TCPConn", connection)
	}
	return tcpConnection
}

func assertEventuallyDialFails(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 20*time.Millisecond)
		if err != nil {
			return
		}
		_ = connection.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener %q still accepts connections", address)
}

func receiveRemoteAddress(t *testing.T, addresses <-chan string) string {
	t.Helper()
	select {
	case address := <-addresses:
		return address
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded remote address")
		return ""
	}
}

type fakeTunnelPasswordSource struct {
	mu     sync.Mutex
	values map[string][]byte
	calls  int
}

type blockingTunnelPasswordSource struct {
	started chan struct{}
	once    sync.Once
	release atomic.Bool
}

func (source *blockingTunnelPasswordSource) Password(ctx context.Context, _ string) ([]byte, error) {
	source.once.Do(func() { close(source.started) })
	if source.release.Load() {
		return []byte("password"), nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type recordingTunnelPasswordSource struct {
	password []byte
	returned []byte
}

type errorTunnelPasswordSource struct {
	returned []byte
}

func (source *errorTunnelPasswordSource) Password(context.Context, string) ([]byte, error) {
	return source.returned, fmt.Errorf("test credential error")
}

func (source *recordingTunnelPasswordSource) Password(context.Context, string) ([]byte, error) {
	source.returned = append([]byte(nil), source.password...)
	return source.returned, nil
}

type delayedCancellationPasswordSource struct {
	started     chan struct{}
	cancelled   chan struct{}
	allowReturn chan struct{}
}

type supersedingTunnelPasswordSource struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	values       map[string][]byte
}

func (source *supersedingTunnelPasswordSource) Password(_ context.Context, credentialRef string) ([]byte, error) {
	if credentialRef == "credential-first" {
		close(source.firstStarted)
		<-source.releaseFirst
	}
	password, exists := source.values[credentialRef]
	if !exists {
		return nil, fmt.Errorf("missing test credential")
	}
	return append([]byte(nil), password...), nil
}

type closeEpochPasswordSource struct {
	calls          atomic.Int64
	firstStarted   chan struct{}
	firstCancelled chan struct{}
	releaseFirst   chan struct{}
}

func (source *closeEpochPasswordSource) Password(ctx context.Context, _ string) ([]byte, error) {
	if source.calls.Add(1) != 1 {
		return []byte("password"), nil
	}
	close(source.firstStarted)
	<-ctx.Done()
	close(source.firstCancelled)
	<-source.releaseFirst
	return []byte("password"), nil
}

func (source *delayedCancellationPasswordSource) Password(ctx context.Context, _ string) ([]byte, error) {
	close(source.started)
	<-ctx.Done()
	close(source.cancelled)
	<-source.allowReturn
	return nil, ctx.Err()
}

func (source *fakeTunnelPasswordSource) Password(_ context.Context, credentialRef string) ([]byte, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	value, exists := source.values[credentialRef]
	if !exists {
		return nil, fmt.Errorf("missing test credential")
	}
	return append([]byte(nil), value...), nil
}

type fakeSSHServer struct {
	listener        net.Listener
	fingerprint     string
	remoteAddresses chan string
	connectionsMu   sync.Mutex
	connections     map[net.Conn]struct{}
	connectionCount atomic.Int64
	channelHandler  func(ssh.Channel)
	done            chan struct{}
	closeOnce       sync.Once
}

func newFakeSSHServer(t *testing.T, password string) *fakeSSHServer {
	t.Helper()
	return newFakeSSHServerWithOptions(t, func(metadata ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
		if metadata.User() != "root" || string(supplied) != password {
			return nil, fmt.Errorf("authentication rejected")
		}
		return nil, nil
	}, nil)
}

func newFakeSSHServerWithPasswordCallback(t *testing.T, callback func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error)) *fakeSSHServer {
	return newFakeSSHServerWithOptions(t, callback, nil)
}

func newFakeSSHServerWithChannelHandler(t *testing.T, password string, handler func(ssh.Channel)) *fakeSSHServer {
	return newFakeSSHServerWithOptions(t, func(metadata ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
		if metadata.User() != "root" || string(supplied) != password {
			return nil, fmt.Errorf("authentication rejected")
		}
		return nil, nil
	}, handler)
}

func newFakeSSHServerWithOptions(
	t *testing.T,
	callback func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error),
	handler func(ssh.Channel),
) *fakeSSHServer {
	t.Helper()
	signer := newFakeSSHSigner(t)
	config := &ssh.ServerConfig{PasswordCallback: callback}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake SSH: %v", err)
	}
	server := &fakeSSHServer{
		listener:        listener,
		fingerprint:     ssh.FingerprintSHA256(signer.PublicKey()),
		remoteAddresses: make(chan string, 1024),
		connections:     make(map[net.Conn]struct{}),
		channelHandler:  handler,
		done:            make(chan struct{}),
	}
	go server.serve(config)
	t.Cleanup(server.close)
	return server
}

func newFakeSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate fake SSH host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create fake SSH signer: %v", err)
	}
	return signer
}

func (server *fakeSSHServer) address() string { return server.listener.Addr().String() }

func (server *fakeSSHServer) serve(config *ssh.ServerConfig) {
	defer close(server.done)
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.connectionsMu.Lock()
		server.connections[connection] = struct{}{}
		server.connectionsMu.Unlock()
		go server.serveConnection(connection, config)
	}
}

func (server *fakeSSHServer) serveConnection(connection net.Conn, config *ssh.ServerConfig) {
	defer func() {
		connection.Close()
		server.connectionsMu.Lock()
		delete(server.connections, connection)
		server.connectionsMu.Unlock()
	}()
	sshConnection, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		return
	}
	server.connectionCount.Add(1)
	defer sshConnection.Close()
	go ssh.DiscardRequests(requests)
	for channelRequest := range channels {
		if channelRequest.ChannelType() != "direct-tcpip" {
			_ = channelRequest.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		var request struct {
			DestinationHost string
			DestinationPort uint32
			OriginHost      string
			OriginPort      uint32
		}
		if err := ssh.Unmarshal(channelRequest.ExtraData(), &request); err != nil {
			_ = channelRequest.Reject(ssh.ConnectionFailed, "invalid forwarding request")
			continue
		}
		server.remoteAddresses <- net.JoinHostPort(request.DestinationHost, strconv.Itoa(int(request.DestinationPort)))
		channel, channelRequests, err := channelRequest.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		if server.channelHandler != nil {
			go server.channelHandler(channel)
		} else {
			go func() {
				defer channel.Close()
				_, _ = io.Copy(channel, channel)
			}()
		}
	}
}

func (server *fakeSSHServer) closeConnections() {
	server.connectionsMu.Lock()
	for connection := range server.connections {
		_ = connection.Close()
	}
	server.connectionsMu.Unlock()
}

func (server *fakeSSHServer) close() {
	server.closeOnce.Do(func() {
		_ = server.listener.Close()
		server.connectionsMu.Lock()
		for connection := range server.connections {
			_ = connection.Close()
		}
		server.connectionsMu.Unlock()
		<-server.done
	})
}
