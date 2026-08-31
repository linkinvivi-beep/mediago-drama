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
	// ErrTunnelSuperseded means a newer connection target replaced this request.
	ErrTunnelSuperseded = errors.New("AutoDL tunnel target was superseded")
	// ErrTunnelStale means Close invalidated work that began before its epoch.
	ErrTunnelStale = errors.New("AutoDL tunnel request is stale")
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
	hooks     tunnelManagerHooks

	mu         sync.Mutex
	instances  map[string]*tunnelInstance
	operations map[*tunnelOperation]struct{}
	closed     bool
	closeDone  chan struct{}
}

type tunnelManagerHooks struct {
	beforeHandshakeWatch func()
	afterHandshake       func()
}

type tunnelInstance struct {
	revision  uint64
	tunnel    *managedTunnel
	operation *tunnelOperation
	closing   bool
	closeDone chan struct{}
}

type tunnelOperation struct {
	instanceID string
	targetKey  string
	revision   uint64
	waiters    int
	done       chan struct{}
	cancel     context.CancelFunc
	ctx        context.Context
	reason     error
	result     Tunnel
	err        error
}

// NewTunnelManager creates a multi-instance tunnel manager. CloseAll is
// terminal; construct a new manager to establish tunnels after shutdown.
func NewTunnelManager(passwords TunnelPasswordSource) TunnelManager {
	return newTunnelManagerWithHooks(passwords, tunnelManagerHooks{})
}

func newTunnelManagerWithHooks(passwords TunnelPasswordSource, hooks tunnelManagerHooks) TunnelManager {
	return &tunnelManager{
		passwords:  passwords,
		hooks:      hooks,
		instances:  make(map[string]*tunnelInstance),
		operations: make(map[*tunnelOperation]struct{}),
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
		instance := manager.instances[target.InstanceProfileID]
		if instance == nil {
			instance = &tunnelInstance{}
			manager.instances[target.InstanceProfileID] = instance
		}
		if instance.closing {
			manager.mu.Unlock()
			return Tunnel{}, ErrTunnelStale
		}
		if current := instance.tunnel; current != nil {
			if current.targetKey == targetKey && current.active.Load() {
				result := current.public()
				manager.mu.Unlock()
				return result, nil
			}
			instance.tunnel = nil
			if current.targetKey != targetKey {
				instance.revision++
			}
			return manager.startTunnelOperationLocked(ctx, target, targetKey, instance, current)
		}
		if current := instance.operation; current != nil {
			if current.targetKey == targetKey && current.revision == instance.revision && current.reason == nil {
				current.waiters++
				manager.mu.Unlock()
				return manager.waitTunnelOperation(ctx, current)
			}
			instance.revision++
			if current.reason == nil {
				current.reason = ErrTunnelSuperseded
			}
			current.cancel()
		}
		return manager.startTunnelOperationLocked(ctx, target, targetKey, instance, nil)
	}
}

func (manager *tunnelManager) startTunnelOperationLocked(
	ctx context.Context,
	target TunnelTarget,
	targetKey string,
	instance *tunnelInstance,
	retired *managedTunnel,
) (Tunnel, error) {
	operationContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	operation := &tunnelOperation{
		instanceID: target.InstanceProfileID,
		targetKey:  targetKey,
		revision:   instance.revision,
		waiters:    1,
		done:       make(chan struct{}),
		cancel:     cancel,
		ctx:        operationContext,
	}
	instance.operation = operation
	manager.operations[operation] = struct{}{}
	manager.mu.Unlock()

	go manager.runTunnelOperation(operation, target, targetKey, retired)
	return manager.waitTunnelOperation(ctx, operation)
}

func (manager *tunnelManager) runTunnelOperation(
	operation *tunnelOperation,
	target TunnelTarget,
	targetKey string,
	retired *managedTunnel,
) {
	if retired != nil {
		retired.closeAndWait()
	}
	created, openErr := openManagedTunnel(operation.ctx, manager.passwords, target, targetKey, manager.hooks)
	manager.finishTunnelOperation(operation, created, openErr)
}

func (manager *tunnelManager) finishTunnelOperation(operation *tunnelOperation, created *managedTunnel, openErr error) {
	manager.mu.Lock()
	instance := manager.instances[operation.instanceID]
	valid := !manager.closed && operation.ctx.Err() == nil && instance != nil && !instance.closing &&
		instance.revision == operation.revision && instance.operation == operation && operation.reason == nil
	if valid && openErr == nil {
		instance.tunnel = created
		operation.result = created.public()
		operation.err = nil
		manager.finalizeTunnelOperationLocked(instance, operation)
		manager.mu.Unlock()
		return
	}
	discardCreated := created != nil
	if !discardCreated {
		operation.err = manager.tunnelOperationErrorLocked(operation, openErr)
		manager.finalizeTunnelOperationLocked(instance, operation)
		manager.mu.Unlock()
		return
	}
	created.active.Store(false)
	manager.mu.Unlock()

	created.closeAndWait()
	manager.mu.Lock()
	instance = manager.instances[operation.instanceID]
	operation.err = manager.tunnelOperationErrorLocked(operation, openErr)
	manager.finalizeTunnelOperationLocked(instance, operation)
	manager.mu.Unlock()
}

func (manager *tunnelManager) tunnelOperationErrorLocked(operation *tunnelOperation, openErr error) error {
	if operation.reason != nil {
		return operation.reason
	}
	if contextErr := operation.ctx.Err(); contextErr != nil {
		return contextErr
	}
	if manager.closed {
		return ErrTunnelManagerClosed
	}
	if instance := manager.instances[operation.instanceID]; instance == nil || instance.closing ||
		instance.revision != operation.revision || instance.operation != operation {
		return ErrTunnelSuperseded
	}
	return openErr
}

func (manager *tunnelManager) finalizeTunnelOperationLocked(instance *tunnelInstance, operation *tunnelOperation) {
	if instance != nil && instance.operation == operation {
		instance.operation = nil
	}
	delete(manager.operations, operation)
	operation.cancel()
	close(operation.done)
}

func (manager *tunnelManager) waitTunnelOperation(ctx context.Context, operation *tunnelOperation) (Tunnel, error) {
	select {
	case <-operation.done:
		manager.releaseTunnelWaiter(operation, false)
		return operation.result, operation.err
	case <-ctx.Done():
		manager.releaseTunnelWaiter(operation, true)
		return Tunnel{}, ctx.Err()
	}
}

func (manager *tunnelManager) releaseTunnelWaiter(operation *tunnelOperation, callerCanceled bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if operation.waiters > 0 {
		operation.waiters--
	}
	if !callerCanceled || operation.waiters != 0 || operation.reason != nil {
		return
	}
	select {
	case <-operation.done:
		return
	default:
		operation.reason = context.Canceled
		operation.cancel()
	}
}

func (manager *tunnelManager) Close(instanceID string) error {
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
	instance := manager.instances[instanceID]
	if instance == nil {
		manager.mu.Unlock()
		return nil
	}
	if instance.closing {
		done := instance.closeDone
		manager.mu.Unlock()
		<-done
		return nil
	}
	instance.revision++
	instance.closing = true
	instance.closeDone = make(chan struct{})
	current := instance.tunnel
	instance.tunnel = nil
	pending := make([]<-chan struct{}, 0)
	for operation := range manager.operations {
		if operation.instanceID != instanceID {
			continue
		}
		operation.reason = ErrTunnelStale
		operation.cancel()
		pending = append(pending, operation.done)
	}
	manager.mu.Unlock()
	if current != nil {
		current.closeAndWait()
	}
	for _, done := range pending {
		<-done
	}
	manager.mu.Lock()
	instance.closing = false
	close(instance.closeDone)
	manager.mu.Unlock()
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
	tunnels := make([]*managedTunnel, 0, len(manager.instances))
	closing := make([]<-chan struct{}, 0)
	for _, instance := range manager.instances {
		if instance.tunnel != nil {
			tunnels = append(tunnels, instance.tunnel)
			instance.tunnel = nil
		}
		if instance.closing {
			closing = append(closing, instance.closeDone)
		}
	}
	pending := make([]<-chan struct{}, 0, len(manager.operations))
	for operation := range manager.operations {
		operation.reason = ErrTunnelManagerClosed
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
	for _, done := range closing {
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

func openManagedTunnel(
	ctx context.Context,
	passwords TunnelPasswordSource,
	target TunnelTarget,
	targetKey string,
	hooks tunnelManagerHooks,
) (*managedTunnel, error) {
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
	handshakeWatcherDone := make(chan struct{})
	var handshakeMu sync.Mutex
	handshakeReturned := false
	go func() {
		defer close(handshakeWatcherDone)
		if hooks.beforeHandshakeWatch != nil {
			hooks.beforeHandshakeWatch()
		}
		select {
		case <-ctx.Done():
			handshakeMu.Lock()
			if !handshakeReturned {
				_ = rawConnection.Close()
			}
			handshakeMu.Unlock()
		case <-handshakeFinished:
		}
	}()
	sshConnection, channels, requests, err := ssh.NewClientConn(rawConnection, serverAddress, config)
	if hooks.afterHandshake != nil {
		hooks.afterHandshake()
	}
	handshakeMu.Lock()
	handshakeReturned = true
	handshakeMu.Unlock()
	close(handshakeFinished)
	<-handshakeWatcherDone
	config.Auth = nil
	if err != nil {
		_ = rawConnection.Close()
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("establish AutoDL SSH session: %w", err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		_ = sshConnection.Close()
		return nil, contextErr
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
	copied := make(chan error, 2)
	go func() {
		copied <- copyAndCloseWrite(remote, local)
	}()
	go func() {
		copied <- copyAndCloseWrite(local, remote)
	}()
	if firstErr := <-copied; firstErr != nil {
		_ = local.Close()
		_ = remote.Close()
	}
	<-copied
}

type closeWriter interface {
	CloseWrite() error
}

func copyAndCloseWrite(destination io.Writer, source io.Reader) error {
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	if halfCloser, ok := destination.(closeWriter); ok {
		return halfCloser.CloseWrite()
	}
	if closer, ok := destination.(io.Closer); ok {
		return closer.Close()
	}
	return nil
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
