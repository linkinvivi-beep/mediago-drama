package keychain

import (
	"context"
	"errors"
	"io"
	"os/exec"
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
	if string(call.stdin) != testSecret+"\n" {
		t.Fatalf("runner stdin = %q, want a newline-terminated secret", call.stdin)
	}
}

func TestGenericPasswordStoreSetEnforcesConservativeSecurityToolLimit(t *testing.T) {
	accepted := strings.Repeat("a", 127)
	runner := &fakeRunner{}
	store := newGenericPasswordStore(runner)
	if err := store.Set(context.Background(), "service", "account", accepted); err != nil {
		t.Fatalf("Set(127 bytes) error = %v", err)
	}
	if got := string(runner.calls[0].stdin); got != accepted+"\n" {
		t.Fatalf("Set(127 bytes) stdin length = %d, want 128 including newline", len(got))
	}

	for _, secret := range []string{
		strings.Repeat("b", 128),
		"line\nfeed",
		"carriage\rreturn",
		"tab\tcharacter",
		"control\x01character",
	} {
		runner := &fakeRunner{}
		if err := newGenericPasswordStore(runner).Set(context.Background(), "service", "account", secret); err == nil {
			t.Fatalf("Set(%q) error = nil", secret)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("invalid secret invoked runner")
		}
	}
}

func TestGenericPasswordStoreSetAcceptsByteBoundedUnicode(t *testing.T) {
	secret := strings.Repeat("密", 42) // 126 UTF-8 bytes.
	runner := &fakeRunner{}
	if err := newGenericPasswordStore(runner).Set(context.Background(), "service", "account", secret); err != nil {
		t.Fatalf("Set(Unicode) error = %v", err)
	}
	if got := string(runner.calls[0].stdin); got != secret+"\n" {
		t.Fatalf("Set(Unicode) did not preserve UTF-8 bytes")
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

func TestGenericPasswordStoreExistsDoesNotRequestSecretValue(t *testing.T) {
	runner := &fakeRunner{}
	store := newGenericPasswordStore(runner)

	exists, err := store.Exists(context.Background(), "app.medialink.autodl", "instance-1")
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !exists {
		t.Fatal("Exists() = false, want true")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	wantArgs := []string{"find-generic-password", "-s", "app.medialink.autodl", "-a", "instance-1"}
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
	invalid := []string{
		"",
		"contains space",
		"contains\x00nul",
		"contains\nnewline",
		"unicode-账户",
		"bidi-\u202etxt",
		"zero-\u200bwidth",
		"-leading-dash",
		strings.Repeat("x", 129),
	}
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

func TestGenericPasswordStoreAcceptsMaximumLengthASCIIIdentifiers(t *testing.T) {
	runner := &fakeRunner{}
	if err := newGenericPasswordStore(runner).Set(
		context.Background(),
		strings.Repeat("s", 128),
		strings.Repeat("a", 128),
		testSecret,
	); err != nil {
		t.Fatalf("Set(128-byte identifiers) error = %v", err)
	}
}

type retainingRunner struct {
	stdin  []byte
	stdout []byte
	stderr []byte
}

func (runner *retainingRunner) Run(_ context.Context, _ string, _ []string, stdin []byte) ([]byte, []byte, error) {
	runner.stdin = stdin
	return runner.stdout, runner.stderr, nil
}

func TestGenericPasswordStoreClearsTransientByteBuffers(t *testing.T) {
	setRunner := &retainingRunner{
		stdout: []byte("unexpected output"),
		stderr: []byte("unexpected diagnostic"),
	}
	if err := newGenericPasswordStore(setRunner).Set(context.Background(), "service", "account", testSecret); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	assertCleared(t, "set stdin", setRunner.stdin)
	assertCleared(t, "set stdout", setRunner.stdout)
	assertCleared(t, "set stderr", setRunner.stderr)

	getRunner := &retainingRunner{
		stdout: []byte(testSecret + "\n"),
		stderr: []byte("unused diagnostic"),
	}
	got, err := newGenericPasswordStore(getRunner).Get(context.Background(), "service", "account")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertCleared(t, "get stdout", getRunner.stdout)
	assertCleared(t, "get stderr", getRunner.stderr)
	if got != testSecret {
		t.Fatalf("Get() = %q after buffer clearing, want %q", got, testSecret)
	}
}

func assertCleared(t *testing.T, name string, data []byte) {
	t.Helper()
	for _, value := range data {
		if value != 0 {
			t.Fatalf("%s retained plaintext bytes", name)
		}
	}
}

type fakeSecurityProcess struct {
	detached bool
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	wantIn   string
}

func (process *fakeSecurityProcess) ConfigureIO(stdin io.Reader, stdout, stderr io.Writer) {
	process.stdin = stdin
	process.stdout = stdout
	process.stderr = stderr
}

func (process *fakeSecurityProcess) DetachFromControllingTTY() {
	process.detached = true
}

func (process *fakeSecurityProcess) Run() error {
	// Simulate security(1)'s getpass behavior when a controlling TTY is
	// available: stdin is consumed only after the runner detached the child.
	if !process.detached {
		return errors.New("would read from controlling tty")
	}
	input, err := io.ReadAll(process.stdin)
	if err != nil {
		return err
	}
	if string(input) != process.wantIn {
		return errors.New("wrong stdin")
	}
	_, _ = io.WriteString(process.stdout, "ok")
	_, _ = io.WriteString(process.stderr, "diagnostic")
	return nil
}

type fakeSecurityCommandFactory struct {
	process *fakeSecurityProcess
	path    string
	args    []string
}

func (factory *fakeSecurityCommandFactory) CommandContext(_ context.Context, path string, args ...string) securityProcess {
	factory.path = path
	factory.args = append([]string(nil), args...)
	return factory.process
}

func TestExecRunnerDetachesSecurityProcessBeforeSupplyingStdin(t *testing.T) {
	process := &fakeSecurityProcess{wantIn: testSecret + "\n"}
	factory := &fakeSecurityCommandFactory{process: process}
	runner := execRunner{factory: factory}

	stdout, stderr, err := runner.Run(
		context.Background(),
		securityExecutable,
		[]string{"add-generic-password", "-w"},
		[]byte(testSecret+"\n"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !process.detached {
		t.Fatal("security process retained its controlling TTY")
	}
	if factory.path != securityExecutable || !reflect.DeepEqual(factory.args, []string{"add-generic-password", "-w"}) {
		t.Fatalf("factory call = %q %#v", factory.path, factory.args)
	}
	if string(stdout) != "ok" || string(stderr) != "diagnostic" {
		t.Fatalf("Run() output = %q, %q", stdout, stderr)
	}
}

func TestExecSecurityProcessStartsNewSession(t *testing.T) {
	command := exec.Command("/usr/bin/true")
	process := &execSecurityProcess{command: command}
	process.DetachFromControllingTTY()
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %#v, want Setsid", command.SysProcAttr)
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
