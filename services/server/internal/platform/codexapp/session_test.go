package codexapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
