// Package codexapp provides a small JSON-RPC client for the bundled Codex app-server.
package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Message is one response or notification emitted by Codex app-server.
type Message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError is the public error shape returned by Codex app-server.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Client is the app-server surface consumed by higher-level services.
type Client interface {
	Call(context.Context, string, any, any) error
	Next(context.Context) (Message, error)
	Close()
}

// Session owns one stdio Codex app-server process.
type Session struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	writeMu sync.Mutex
	nextID  int

	stateMu   sync.Mutex
	waiters   map[int]chan Message
	pending   []Message
	notify    chan struct{}
	readDone  chan struct{}
	readErr   error
	closeOnce sync.Once
}

var appServerCommandContext = exec.CommandContext

// Start launches and initializes a Codex app-server session.
func Start(parent context.Context, binPath string) (*Session, error) {
	return StartWithInitContext(parent, parent, binPath)
}

// StartWithInitContext uses parent for the child process lifetime and initCtx
// only for the initialize handshake. Canceling initCtx after a successful start
// does not terminate the app-server process.
func StartWithInitContext(parent context.Context, initCtx context.Context, binPath string) (*Session, error) {
	if strings.TrimSpace(binPath) == "" {
		return nil, fmt.Errorf("Codex executable is required")
	}
	if parent == nil {
		parent = context.Background()
	}
	if initCtx == nil {
		initCtx = parent
	}
	ctx, cancel := context.WithCancel(parent)
	cmd := appServerCommandContext(ctx, binPath, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening app-server stdout: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting app-server: %w", err)
	}
	session := &Session{
		cancel:   cancel,
		cmd:      cmd,
		stdin:    stdin,
		waiters:  make(map[int]chan Message),
		notify:   make(chan struct{}, 1),
		readDone: make(chan struct{}),
	}
	go session.readLoop(bufio.NewScanner(stdout))
	if err := session.initialize(initCtx); err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}

func (session *Session) initialize(ctx context.Context) error {
	var ignored map[string]any
	if err := session.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "medialink", "title": "MediaLink", "version": "1"},
	}, &ignored); err != nil {
		return fmt.Errorf("initializing app-server: %w", err)
	}
	return session.write(map[string]any{"method": "initialized"})
}

// Call sends one request and waits for its matching response.
func (session *Session) Call(ctx context.Context, method string, params any, output any) error {
	session.writeMu.Lock()
	// This check is the cancellation boundary for submission. Cancellation after
	// it races with the already-authorized write, but a pre-canceled call never writes.
	if err := ctx.Err(); err != nil {
		session.writeMu.Unlock()
		return err
	}
	session.nextID++
	id := session.nextID
	response := make(chan Message, 1)
	session.stateMu.Lock()
	session.waiters[id] = response
	session.stateMu.Unlock()
	request := map[string]any{"id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := session.writeUnlocked(request); err != nil {
		session.writeMu.Unlock()
		session.removeWaiter(id, response)
		return err
	}
	session.writeMu.Unlock()
	defer session.removeWaiter(id, response)

	var message Message
	select {
	case message = <-response:
	case <-ctx.Done():
		return ctx.Err()
	case <-session.readDone:
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case message = <-response:
		default:
			return session.readerError()
		}
	}
	if message.Error != nil {
		return fmt.Errorf("app-server request failed (%d): %s", message.Error.Code, safeRPCMessage(message.Error.Message))
	}
	if output == nil || len(message.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(message.Result, output); err != nil {
		return fmt.Errorf("decoding app-server response: %w", err)
	}
	return nil
}

func safeRPCMessage(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.Index(strings.ToLower(value), "http"); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "request failed"
	}
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

// Next returns the next queued or newly received app-server message.
func (session *Session) Next(ctx context.Context) (Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		if message, ok := session.popPending(); ok {
			return message, nil
		}
		select {
		case <-ctx.Done():
			return Message{}, ctx.Err()
		case <-session.notify:
			continue
		case <-session.readDone:
			if err := ctx.Err(); err != nil {
				return Message{}, err
			}
			if message, ok := session.popPending(); ok {
				return message, nil
			}
			return Message{}, session.readerError()
		}
	}
}

func (session *Session) readLoop(scanner *bufio.Scanner) {
	for scanner.Scan() {
		var message Message
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			session.finishRead(fmt.Errorf("decoding app-server message: %w", err))
			return
		}
		session.route(message)
	}
	if err := scanner.Err(); err != nil {
		session.finishRead(fmt.Errorf("reading app-server response: %w", err))
		return
	}
	session.finishRead(io.EOF)
}

func (session *Session) write(value any) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return session.writeUnlocked(value)
}

func (session *Session) writeUnlocked(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding app-server request: %w", err)
	}
	if _, err := session.stdin.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("writing app-server request: %w", err)
	}
	return nil
}

func (session *Session) route(message Message) {
	var responseID int
	if message.Method == "" && (len(message.Result) > 0 || message.Error != nil) && len(message.ID) > 0 && json.Unmarshal(message.ID, &responseID) == nil {
		session.stateMu.Lock()
		waiter := session.waiters[responseID]
		session.stateMu.Unlock()
		if waiter != nil {
			waiter <- message
			return
		}
	}
	session.stateMu.Lock()
	session.pending = append(session.pending, message)
	session.stateMu.Unlock()
	select {
	case session.notify <- struct{}{}:
	default:
	}
}

func (session *Session) popPending() (Message, bool) {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if len(session.pending) == 0 {
		return Message{}, false
	}
	message := session.pending[0]
	session.pending = session.pending[1:]
	return message, true
}

func (session *Session) removeWaiter(id int, waiter chan Message) {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if session.waiters[id] == waiter {
		delete(session.waiters, id)
	}
}

func (session *Session) finishRead(err error) {
	session.stateMu.Lock()
	session.readErr = err
	session.stateMu.Unlock()
	close(session.readDone)
}

func (session *Session) readerError() error {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if session.readErr != nil {
		return session.readErr
	}
	return io.EOF
}

// Close stops the app-server process and releases its pipes.
func (session *Session) Close() {
	if session == nil {
		return
	}
	session.closeOnce.Do(func() {
		session.cancel()
		session.writeMu.Lock()
		_ = session.stdin.Close()
		session.writeMu.Unlock()
		if session.cmd != nil && session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
		}
		if session.cmd != nil {
			_ = session.cmd.Wait()
		}
		<-session.readDone
	})
}
