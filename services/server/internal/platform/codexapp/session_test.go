package codexapp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMessageScannerAcceptsLargeBoundedJSONLine(t *testing.T) {
	payload := strings.Repeat("x", 128<<10)
	scanner := newMessageScanner(strings.NewReader(`{"method":"item/completed","params":"`+payload+`"}`+"\n"), 256<<10)
	if !scanner.Scan() {
		t.Fatalf("Scan() = false, error = %v", scanner.Err())
	}
}

func TestMessageScannerRejectsLineOverExplicitLimit(t *testing.T) {
	scanner := newMessageScanner(strings.NewReader(strings.Repeat("x", 4097)), 4096)
	scanned := scanner.Scan()
	if scanned || scanner.Err() == nil || !strings.Contains(scanner.Err().Error(), "token too long") {
		t.Fatalf("Scan() = %v, error = %v", scanned, scanner.Err())
	}
}

func TestStartWithInitContextDeadlineReapsSilentChild(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do sleep 10; done
`)
	originalCommand := appServerCommandContext
	var command *exec.Cmd
	appServerCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		command = originalCommand(ctx, name, args...)
		return command
	}
	defer func() { appServerCommandContext = originalCommand }()
	initCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := StartWithInitContext(context.Background(), initCtx, binPath)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StartWithInitContext() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline returned after %v", elapsed)
	}
	if command == nil || command.Process == nil {
		t.Fatal("child process was not started")
	}
	if signalErr := command.Process.Signal(syscall.Signal(0)); signalErr == nil {
		t.Fatalf("silent child %d is still alive", command.Process.Pid)
	}
}

func TestStartWithInitContextKeepsProcessAfterSuccessfulInitialization(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/alive"'*) echo '{"id":2,"result":{"alive":true}}' ;;
  esac
done
`)
	initCtx, cancelInit := context.WithCancel(context.Background())
	session, err := StartWithInitContext(context.Background(), initCtx, binPath)
	if err != nil {
		t.Fatalf("StartWithInitContext() error = %v", err)
	}
	defer session.Close()
	cancelInit()
	var result struct {
		Alive bool `json:"alive"`
	}
	if err := session.Call(context.Background(), "test/alive", nil, &result); err != nil || !result.Alive {
		t.Fatalf("Call() = %#v, %v", result, err)
	}
}

func TestSessionInitializesCallsAndQueuesNotifications(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"userAgent":"fake"}}'
      ;;
    *'"method":"test/call"'*)
      echo '{"method":"test/notification","params":{"value":"queued"}}'
      echo '{"id":2,"result":{"value":"done"}}'
      ;;
  esac
done
`)

	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	var response struct {
		Value string `json:"value"`
	}
	if err := session.Call(context.Background(), "test/call", map[string]string{"input": "ok"}, &response); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if response.Value != "done" {
		t.Fatalf("Call() response = %#v", response)
	}
	message, err := session.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if message.Method != "test/notification" || !strings.Contains(string(message.Params), "queued") {
		t.Fatalf("Next() = %#v", message)
	}
}

func TestSessionReturnsRPCError(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/fail"'*) echo '{"id":2,"error":{"code":-32000,"message":"request failed"}}' ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	err = session.Call(context.Background(), "test/fail", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("Call() error = %v", err)
	}
}

func TestSessionUsesMediaLinkClientInfo(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*'"name":"medialink"'*'"title":"MediaLink"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"initialize"'*) echo '{"id":1,"error":{"code":-32602,"message":"wrong client info"}}' ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session.Close()
}

func TestSessionNextCancellationDoesNotDiscardFutureMessages(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/release"'*)
      echo '{"id":2,"result":{"value":"released"}}'
      echo '{"method":"test/notification","params":{"value":"after-cancel"}}'
      ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, nextErr := session.Next(ctx)
		result <- nextErr
	}()
	<-started
	// Give Next a scheduling turn to enter the silent transport read before cancellation.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case nextErr := <-result:
		if !errors.Is(nextErr, context.Canceled) {
			t.Fatalf("Next() error = %v, want context.Canceled", nextErr)
		}
	case <-time.After(250 * time.Millisecond):
		session.Close()
		t.Fatal("Next() did not return promptly after cancellation")
	}

	var response struct {
		Value string `json:"value"`
	}
	if err := session.Call(context.Background(), "test/release", struct{}{}, &response); err != nil {
		t.Fatalf("Call() after canceled Next error = %v", err)
	}
	if response.Value != "released" {
		t.Fatalf("Call() response = %#v", response)
	}
	message, err := session.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() after cancellation error = %v", err)
	}
	if message.Method != "test/notification" || !strings.Contains(string(message.Params), "after-cancel") {
		t.Fatalf("Next() after cancellation = %#v", message)
	}
}

func TestSessionCallCanCompleteWhileNextWaits(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/interrupt"'*) echo '{"id":2,"result":{"accepted":true}}' ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	nextCtx, cancelNext := context.WithCancel(context.Background())
	nextResult := make(chan error, 1)
	go func() {
		_, nextErr := session.Next(nextCtx)
		nextResult <- nextErr
	}()
	time.Sleep(10 * time.Millisecond)

	callResult := make(chan error, 1)
	go func() {
		var response struct {
			Accepted bool `json:"accepted"`
		}
		callErr := session.Call(context.Background(), "test/interrupt", struct{}{}, &response)
		if callErr == nil && !response.Accepted {
			callErr = errors.New("interrupt response was not accepted")
		}
		callResult <- callErr
	}()
	select {
	case callErr := <-callResult:
		if callErr != nil {
			t.Fatalf("concurrent Call() error = %v", callErr)
		}
	case <-time.After(250 * time.Millisecond):
		cancelNext()
		session.Close()
		t.Fatal("Call() was blocked by a waiting Next()")
	}
	cancelNext()
	if nextErr := <-nextResult; !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("Next() error = %v, want context.Canceled", nextErr)
	}
}

func TestSessionPreCanceledCallWritesNoRequest(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
canceled_requests=0
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/canceled"'*) canceled_requests=$((canceled_requests + 1)) ;;
    *'"method":"test/count"'*)
      case "$line" in
        *'"id":2'*) echo "{\"id\":2,\"result\":{\"count\":$canceled_requests}}" ;;
        *'"id":3'*) echo "{\"id\":3,\"result\":{\"count\":$canceled_requests}}" ;;
      esac
      ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Call(canceled, "test/canceled", struct{}{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Call() error = %v, want context.Canceled", err)
	}
	var response struct {
		Count int `json:"count"`
	}
	if err := session.Call(context.Background(), "test/count", struct{}{}, &response); err != nil {
		t.Fatalf("count Call() error = %v", err)
	}
	if response.Count != 0 {
		t.Fatalf("canceled request count = %d, want 0", response.Count)
	}
}

func TestSessionParentCancellationIsReturnedInsteadOfEOF(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
  esac
done
`)
	parent, cancel := context.WithCancel(context.Background())
	session, err := Start(parent, binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()
	cancel()
	<-session.readDone

	if _, err := session.Next(parent); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() after parent cancellation error = %v, want context.Canceled", err)
	}
	if err := session.Call(parent, "test/never-written", struct{}{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() after parent cancellation error = %v, want context.Canceled", err)
	}
}

func TestSessionCallReadDonePrefersContextCancellation(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/block"'*) echo '{"method":"test/received","params":{}}' ;;
    *'"method":"test/exit"'*) exit 0 ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	callContext := &controlledErrorContext{}
	callResult := make(chan error, 1)
	go func() {
		callResult <- session.Call(callContext, "test/block", struct{}{}, nil)
	}()
	message, err := session.Next(context.Background())
	if err != nil || message.Method != "test/received" {
		t.Fatalf("Next() = %#v, %v, want test/received", message, err)
	}
	callContext.setError(context.Canceled)
	_ = session.Call(context.Background(), "test/exit", struct{}{}, nil)
	if err := <-callResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() readDone error = %v, want context.Canceled", err)
	}
}

func TestSessionServerRequestWithResponseIDCollisionRemainsPending(t *testing.T) {
	binPath := writeFakeAppServer(t, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"test/collision"'*)
      echo '{"id":2,"method":"server/request","params":{"question":"confirm"}}'
      echo '{"id":2,"result":{"value":"done"}}'
      ;;
  esac
done
`)
	session, err := Start(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	var response struct {
		Value string `json:"value"`
	}
	if err := session.Call(context.Background(), "test/collision", struct{}{}, &response); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if response.Value != "done" {
		t.Fatalf("Call() response = %#v", response)
	}
	message, err := session.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if message.Method != "server/request" || string(message.ID) != "2" {
		t.Fatalf("Next() = %#v, want colliding server request", message)
	}
}

type controlledErrorContext struct {
	mu  sync.Mutex
	err error
}

func (*controlledErrorContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*controlledErrorContext) Done() <-chan struct{}       { return nil }
func (*controlledErrorContext) Value(any) any               { return nil }

func (ctx *controlledErrorContext) Err() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	return ctx.err
}

func (ctx *controlledErrorContext) setError(err error) {
	ctx.mu.Lock()
	ctx.err = err
	ctx.mu.Unlock()
}

func writeFakeAppServer(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server fixture uses a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
