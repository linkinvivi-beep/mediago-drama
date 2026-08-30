package autodl

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

var (
	// ErrTunnelManagerClosed means CloseAll has permanently stopped a manager.
	ErrTunnelManagerClosed = errors.New("AutoDL tunnel manager is closed")
	// ErrHostKeyMismatch means the SSH server did not present the pinned key.
	ErrHostKeyMismatch = errors.New("AutoDL SSH host key mismatch")
)

// TunnelTarget contains the non-secret connection fields needed to establish
// one AutoDL local-forwarding session.
type TunnelTarget struct {
	InstanceProfileID string
	Host              string
	SSHPort           int
	SSHUser           string
	ComfyPort         int
	HostFingerprint   string
	CredentialRef     string
}

// Tunnel is the loopback HTTP origin assigned to one AutoDL instance.
type Tunnel struct {
	InstanceProfileID string
	BaseURL           string
}

// TunnelPasswordSource transfers ownership of returned password bytes to the
// caller. Implementations should return a fresh slice for each call.
type TunnelPasswordSource interface {
	Password(context.Context, string) ([]byte, error)
}

// TunnelManager owns independent SSH sessions and loopback listeners for
// AutoDL instances.
type TunnelManager interface {
	Ensure(context.Context, TunnelTarget) (Tunnel, error)
	Close(string) error
	CloseAll() error
}

type tunnelManager struct {
	passwords TunnelPasswordSource

	mu         sync.Mutex
	tunnels    map[string]*managedTunnel
	operations map[string]*tunnelOperation
	closed     bool
	closeDone  chan struct{}
}

type tunnelOperation struct {
	done    chan struct{}
	cancel  context.CancelFunc
	invalid bool
}

// NewTunnelManager creates a multi-instance tunnel manager. CloseAll is
// terminal; construct a new manager to establish tunnels after shutdown.
func NewTunnelManager(passwords TunnelPasswordSource) TunnelManager {
	return &tunnelManager{
		passwords:  passwords,
		tunnels:    make(map[string]*managedTunnel),
		operations: make(map[string]*tunnelOperation),
		closeDone:  make(chan struct{}),
	}
}

func (manager *tunnelManager) Ensure(ctx context.Context, target TunnelTarget) (Tunnel, error) {
	if ctx == nil {
		return Tunnel{}, fmt.Errorf("AutoDL tunnel context is required")
	}
	if err := ctx.Err(); err != nil {
		return Tunnel{}, err
	}
	if err := validateTunnelTarget(target); err != nil {
		return Tunnel{}, err
	}
	if manager == nil || manager.passwords == nil {
		return Tunnel{}, fmt.Errorf("AutoDL tunnel password source is unavailable")
	}
	targetKey := tunnelTargetKey(target)

	for {
		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return Tunnel{}, ErrTunnelManagerClosed
		}
		if current := manager.tunnels[target.InstanceProfileID]; current != nil {
			if current.targetKey == targetKey && current.active.Load() {
				result := current.public()
				manager.mu.Unlock()
				return result, nil
			}
			delete(manager.tunnels, target.InstanceProfileID)
			retirement := &tunnelOperation{done: make(chan struct{}), cancel: func() {}}
			manager.operations[target.InstanceProfileID] = retirement
			manager.mu.Unlock()
			current.closeAndWait()
			manager.mu.Lock()
			if manager.operations[target.InstanceProfileID] == retirement {
				delete(manager.operations, target.InstanceProfileID)
			}
			close(retirement.done)
			manager.mu.Unlock()
			continue
		}
		if operation := manager.operations[target.InstanceProfileID]; operation != nil {
			done := operation.done
			manager.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return Tunnel{}, ctx.Err()
			}
		}

		operationContext, cancel := context.WithCancel(ctx)
		operation := &tunnelOperation{done: make(chan struct{}), cancel: cancel}
		manager.operations[target.InstanceProfileID] = operation
		manager.mu.Unlock()

		created, err := openManagedTunnel(operationContext, manager.passwords, target, targetKey)
		cancel()

		manager.mu.Lock()
		if manager.operations[target.InstanceProfileID] == operation {
			delete(manager.operations, target.InstanceProfileID)
		}
		shouldClose := false
		if err == nil && !manager.closed && !operation.invalid {
			manager.tunnels[target.InstanceProfileID] = created
		} else if err == nil {
			shouldClose = true
			if manager.closed {
				err = ErrTunnelManagerClosed
			} else {
				err = context.Canceled
			}
		}
		manager.mu.Unlock()
		if shouldClose {
			created.closeAndWait()
			created = nil
		}
		close(operation.done)
		if err != nil {
			if created != nil {
				created.closeAndWait()
			}
			if contextErr := ctx.Err(); contextErr != nil {
				return Tunnel{}, contextErr
			}
			return Tunnel{}, err
		}
		return created.public(), nil
	}
}

func (manager *tunnelManager) Close(instanceID string) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	current := manager.tunnels[instanceID]
	delete(manager.tunnels, instanceID)
	var pending <-chan struct{}
	if operation := manager.operations[instanceID]; operation != nil {
		operation.invalid = true
		operation.cancel()
		pending = operation.done
	}
	manager.mu.Unlock()
	if current != nil {
		current.closeAndWait()
	}
	if pending != nil {
		<-pending
	}
	return nil
}

func (manager *tunnelManager) CloseAll() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		done := manager.closeDone
		manager.mu.Unlock()
		<-done
		return nil
	}
	manager.closed = true
	tunnels := make([]*managedTunnel, 0, len(manager.tunnels))
	for _, current := range manager.tunnels {
		tunnels = append(tunnels, current)
	}
	clear(manager.tunnels)
	pending := make([]<-chan struct{}, 0, len(manager.operations))
	for _, operation := range manager.operations {
		operation.invalid = true
		operation.cancel()
		pending = append(pending, operation.done)
	}
	manager.mu.Unlock()
	for _, current := range tunnels {
		current.closeAndWait()
	}
	for _, done := range pending {
		<-done
	}
	close(manager.closeDone)
	return nil
}

func validateTunnelTarget(target TunnelTarget) error {
	if !validTunnelIdentifier(target.InstanceProfileID) {
		return fmt.Errorf("invalid AutoDL instance profile id")
	}
	if !validSSHHost(target.Host) || !validSSHUser(target.SSHUser) {
		return fmt.Errorf("invalid AutoDL SSH target")
	}
	if target.SSHPort < 1 || target.SSHPort > 65535 || target.ComfyPort < 1 || target.ComfyPort > 65535 {
		return fmt.Errorf("invalid AutoDL tunnel port")
	}
	if !validTunnelIdentifier(target.CredentialRef) {
		return fmt.Errorf("invalid AutoDL credential reference")
	}
	const fingerprintPrefix = "SHA256:"
	if len(target.HostFingerprint) <= len(fingerprintPrefix) || target.HostFingerprint[:len(fingerprintPrefix)] != fingerprintPrefix {
		return fmt.Errorf("invalid AutoDL SSH host fingerprint")
	}
	digest, err := base64.RawStdEncoding.DecodeString(target.HostFingerprint[len(fingerprintPrefix):])
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("invalid AutoDL SSH host fingerprint")
	}
	return nil
}

func validTunnelIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if index == 0 {
			if !isASCIILetter(char) && !isASCIIDigit(char) {
				return false
			}
			continue
		}
		if !isASCIILetter(char) && !isASCIIDigit(char) && char != '.' && char != '_' && char != ':' && char != '-' {
			return false
		}
	}
	return true
}

func tunnelTargetKey(target TunnelTarget) string {
	return target.Host + "\x00" + strconv.Itoa(target.SSHPort) + "\x00" + target.SSHUser + "\x00" +
		strconv.Itoa(target.ComfyPort) + "\x00" + target.HostFingerprint + "\x00" + target.CredentialRef
}

func openManagedTunnel(ctx context.Context, passwords TunnelPasswordSource, target TunnelTarget, targetKey string) (*managedTunnel, error) {
	password, err := passwords.Password(ctx, target.CredentialRef)
	defer zeroBytes(password)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("load AutoDL SSH password: %w", err)
	}
	if len(password) == 0 {
		return nil, fmt.Errorf("AutoDL SSH password is empty")
	}

	serverAddress := net.JoinHostPort(target.Host, strconv.Itoa(target.SSHPort))
	rawConnection, err := (&net.Dialer{}).DialContext(ctx, "tcp", serverAddress)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("connect AutoDL SSH: %w", err)
	}
	config := &ssh.ClientConfig{
		User: target.SSHUser,
		Auth: []ssh.AuthMethod{ssh.Password(string(password))},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented := ssh.FingerprintSHA256(key)
			if subtle.ConstantTimeCompare([]byte(presented), []byte(target.HostFingerprint)) != 1 {
				return ErrHostKeyMismatch
			}
			return nil
		},
	}
	handshakeFinished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = rawConnection.Close()
		case <-handshakeFinished:
		}
	}()
	sshConnection, channels, requests, err := ssh.NewClientConn(rawConnection, serverAddress, config)
	close(handshakeFinished)
	config.Auth = nil
	if err != nil {
		_ = rawConnection.Close()
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("establish AutoDL SSH session: %w", err)
	}
	client := ssh.NewClient(sshConnection, channels, requests)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("listen for AutoDL tunnel: %w", err)
	}
	managed := &managedTunnel{
		instanceID:  target.InstanceProfileID,
		targetKey:   targetKey,
		remotePort:  target.ComfyPort,
		client:      client,
		listener:    listener,
		locals:      make(map[net.Conn]struct{}),
		done:        make(chan struct{}),
		monitorDone: make(chan struct{}),
	}
	managed.active.Store(true)
	managed.workers.Add(1)
	go managed.acceptLoop()
	go func() {
		defer close(managed.monitorDone)
		_ = client.Wait()
		managed.shutdown()
	}()
	return managed, nil
}

type managedTunnel struct {
	instanceID string
	targetKey  string
	remotePort int
	client     *ssh.Client
	listener   net.Listener

	active       atomic.Bool
	shutdownOnce sync.Once
	done         chan struct{}
	monitorDone  chan struct{}
	workers      sync.WaitGroup
	localsMu     sync.Mutex
	locals       map[net.Conn]struct{}
}

func (tunnel *managedTunnel) public() Tunnel {
	return Tunnel{InstanceProfileID: tunnel.instanceID, BaseURL: "http://" + tunnel.listener.Addr().String()}
}

func (tunnel *managedTunnel) acceptLoop() {
	defer tunnel.workers.Done()
	for {
		local, err := tunnel.listener.Accept()
		if err != nil {
			return
		}
		tunnel.localsMu.Lock()
		tunnel.locals[local] = struct{}{}
		tunnel.localsMu.Unlock()
		tunnel.workers.Add(1)
		go tunnel.forward(local)
	}
}

func (tunnel *managedTunnel) forward(local net.Conn) {
	defer tunnel.workers.Done()
	defer func() {
		_ = local.Close()
		tunnel.localsMu.Lock()
		delete(tunnel.locals, local)
		tunnel.localsMu.Unlock()
	}()
	remoteAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnel.remotePort))
	remote, err := tunnel.client.Dial("tcp", remoteAddress)
	if err != nil {
		return
	}
	defer remote.Close()
	copied := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, local)
		copied <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(local, remote)
		copied <- struct{}{}
	}()
	<-copied
	_ = local.Close()
	_ = remote.Close()
	<-copied
}

func (tunnel *managedTunnel) shutdown() {
	tunnel.shutdownOnce.Do(func() {
		tunnel.active.Store(false)
		_ = tunnel.listener.Close()
		_ = tunnel.client.Close()
		tunnel.localsMu.Lock()
		for local := range tunnel.locals {
			_ = local.Close()
		}
		tunnel.localsMu.Unlock()
		close(tunnel.done)
	})
}

func (tunnel *managedTunnel) closeAndWait() {
	tunnel.shutdown()
	tunnel.workers.Wait()
	<-tunnel.done
	<-tunnel.monitorDone
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
