package keychain

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testSecret = "correct horse battery staple"

type runnerCall struct {
	path  string
	args  []string
	stdin []byte
}

type fakeRunner struct {
	calls  []runnerCall
	stdout []byte
	stderr []byte
	err    error
}

func (runner *fakeRunner) Run(_ context.Context, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, runnerCall{
		path:  path,
		args:  append([]string(nil), args...),
		stdin: append([]byte(nil), stdin...),
	})
	return append([]byte(nil), runner.stdout...), append([]byte(nil), runner.stderr...), runner.err
}

type fakeExitError struct {
	code    int
	message string
}

func (err fakeExitError) Error() string { return err.message }
func (err fakeExitError) ExitCode() int { return err.code }

func TestGenericPasswordStoreSetPassesSecretOnlyOnStdin(t *testing.T) {
	runner := &fakeRunner{}
	store := newGenericPasswordStore(runner)

	if err := store.Set(context.Background(), "app.medialink.autodl", "instance-1", testSecret); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.path != "/usr/bin/security" {
		t.Fatalf("runner path = %q", call.path)
	}
	wantArgs := []string{"add-generic-password", "-U", "-s", "app.medialink.autodl", "-a", "instance-1", "-w"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", call.args, wantArgs)
	}
	if strings.Contains(strings.Join(call.args, " "), testSecret) {
		t.Fatal("secret appeared in process arguments")
	}
	if string(call.stdin) != testSecret {
		t.Fatalf("runner stdin did not receive the exact secret")
	}
}

func TestGenericPasswordStoreGetReturnsSecretWithoutOnlyTerminalNewline(t *testing.T) {
	runner := &fakeRunner{stdout: []byte("line one\nline two\r\n")}
	store := newGenericPasswordStore(runner)

	got, err := store.Get(context.Background(), "app.medialink.autodl", "instance-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != "line one\nline two" {
		t.Fatalf("Get() = %q, want embedded newline preserved", got)
	}
	wantArgs := []string{"find-generic-password", "-s", "app.medialink.autodl", "-a", "instance-1", "-w"}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.calls[0].args, wantArgs)
	}
}

func TestGenericPasswordStoreErrorsNeverExposeRunnerOutputOrSecret(t *testing.T) {
	operations := []struct {
		name string
		run  func(*genericPasswordStore) error
	}{
		{name: "set", run: func(store *genericPasswordStore) error {
			return store.Set(context.Background(), "app.medialink.autodl", "instance-1", testSecret)
		}},
		{name: "get", run: func(store *genericPasswordStore) error {
			_, err := store.Get(context.Background(), "app.medialink.autodl", "instance-1")
			return err
		}},
		{name: "delete", run: func(store *genericPasswordStore) error {
			return store.Delete(context.Background(), "app.medialink.autodl", "instance-1")
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			runner := &fakeRunner{
				stderr: []byte("diagnostic contains " + testSecret),
				err:    errors.New("runner contains " + testSecret),
			}
			err := operation.run(newGenericPasswordStore(runner))
			if err == nil {
				t.Fatal("operation error = nil")
			}
			if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), "diagnostic contains") {
				t.Fatalf("public error leaked runner data: %v", err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("runner calls = %d, want 1", len(runner.calls))
			}
			if strings.Contains(strings.Join(runner.calls[0].args, " "), testSecret) {
				t.Fatal("secret appeared in process arguments")
			}
		})
	}
}

func TestGenericPasswordStoreDeleteTreatsMissingItemAsSuccess(t *testing.T) {
	runner := &fakeRunner{err: fakeExitError{code: keychainItemNotFoundExitCode, message: "missing"}}
	store := newGenericPasswordStore(runner)

	if err := store.Delete(context.Background(), "app.medialink.autodl", "instance-1"); err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
	if _, err := store.Get(context.Background(), "app.medialink.autodl", "instance-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestGenericPasswordStoreRejectsInvalidIdentifiersWithoutRunningCommand(t *testing.T) {
	invalid := []string{"", "   ", "contains\x00nul", "contains\nnewline", strings.Repeat("x", maxIdentifierBytes+1)}
	for _, value := range invalid {
		runner := &fakeRunner{}
		store := newGenericPasswordStore(runner)
		if err := store.Set(context.Background(), value, "account", testSecret); err == nil {
			t.Fatalf("Set(service=%q) error = nil", value)
		}
		if err := store.Set(context.Background(), "service", value, testSecret); err == nil {
			t.Fatalf("Set(account=%q) error = nil", value)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("invalid identifier %q invoked runner", value)
		}
	}
}

func TestGenericPasswordStoreHonorsCancelledContextWithoutRunningCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{}
	store := newGenericPasswordStore(runner)

	if err := store.Set(ctx, "service", "account", testSecret); !errors.Is(err, context.Canceled) {
		t.Fatalf("Set(cancelled) error = %v, want context.Canceled", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("cancelled operation invoked runner %d times", len(runner.calls))
	}
}
