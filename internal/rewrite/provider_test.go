package rewrite

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// keyringStub stands in for secret-tool: CI has no keyring, and a gate has no business writing to
// the developer's own. It records every invocation, because the identity of the item is the part
// that has to be right — a store and a lookup that disagree about their attributes write a key that
// nothing can read back.
type keyringStub struct {
	calls [][]string
	cmds  []*exec.Cmd
	// What a store was handed on stdin, written by `tee`: the reader the real code passes is drained
	// by Run(), so asking it afterwards can only ever prove nothing.
	storePath string
}

func (s *keyringStub) storedSecret(t *testing.T) string {
	t.Helper()
	if s.storePath == "" {
		return ""
	}
	b, err := os.ReadFile(s.storePath)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *keyringStub) storeCmd() *exec.Cmd {
	for i, args := range s.calls {
		if len(args) > 0 && args[0] == "store" {
			return s.cmds[i]
		}
	}
	return nil
}

// stubKeyring refuses to use a shell: every command is a real binary with the value as an argument,
// exactly like the call it replaces, so nothing here could interpret a secret as syntax.
func stubKeyring(t *testing.T, stored string, lookupOK, storeOK bool) *keyringStub {
	t.Helper()
	stub := &keyringStub{storePath: filepath.Join(t.TempDir(), "stored-secret")}
	keyringExec = func(ctx context.Context, args ...string) *exec.Cmd {
		stub.calls = append(stub.calls, args)
		action := ""
		if len(args) > 0 {
			action = args[0]
		}
		var cmd *exec.Cmd
		switch {
		case action == "lookup" && lookupOK:
			cmd = exec.Command("/bin/echo", stored) // the value, printed
		case action == "store" && storeOK:
			cmd = exec.Command("tee", stub.storePath) // keeps what the store is given
		default:
			cmd = exec.Command("false") // no secret-tool, or nothing stored
		}
		stub.cmds = append(stub.cmds, cmd)
		return cmd
	}
	t.Cleanup(func() {
		keyringExec = func(ctx context.Context, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "secret-tool", args...)
		}
	})
	return stub
}

// setup gives each case a private config directory, an optional key file, and an empty cache.
func setup(t *testing.T, keyFile string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if keyFile != "" {
		path := KeyFilePath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(keyFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keyringCached.Lock()
	keyringCached.read, keyringCached.key = false, ""
	keyringCached.Unlock()
}

func TestKeyPrecedence(t *testing.T) {
	env := KeyEnvFor("openai")
	if env == "" {
		t.Fatal("the openai preset must name an environment variable for this test to mean anything")
	}

	// The environment still wins: a key set that way keeps working, and keeps meaning what it meant.
	setup(t, "from-the-file\n")
	stubKeyring(t, "from-the-keyring", true, true)
	t.Setenv(env, "from-the-environment")
	if got := KeySourceFor("openai"); got != "env" {
		t.Fatalf("environment should win, got source %q", got)
	}
	if got := APIKeyFor("openai"); got != "from-the-environment" {
		t.Fatalf("environment key not used: %q", got)
	}

	// Keyring beats the file: it is the newer and better home for a secret.
	setup(t, "from-the-file\n")
	stubKeyring(t, "from-the-keyring", true, true)
	t.Setenv(env, "")
	if got := KeySourceFor("openai"); got != "keyring" {
		t.Fatalf("keyring should beat the file, got source %q", got)
	}
	if got := APIKeyFor("openai"); got != "from-the-keyring" {
		t.Fatalf("keyring key not used: %q", got)
	}

	// A keyring that answers "nothing stored" leaves the file in charge.
	setup(t, "from-the-file\n")
	stubKeyring(t, "", true, true)
	t.Setenv(env, "")
	if got := KeySourceFor("openai"); got != "file" {
		t.Fatalf("the file should be used when the keyring is empty, got %q", got)
	}

	// A machine with no secret-tool at all is not an error either: the file is still read.
	setup(t, "from-the-file\n")
	stubKeyring(t, "", false, false)
	if got := KeySourceFor("openai"); got != "file" {
		t.Fatalf("a missing secret-tool must fall through to the file, got %q", got)
	}

	// No key anywhere is an empty source, not a wrong one.
	setup(t, "")
	stubKeyring(t, "", false, false)
	if got := KeySourceFor("openai"); got != "" {
		t.Fatalf("no key should report no source, got %q", got)
	}
}

// A backend that takes no key must not spawn a keyring lookup at all: a local model needs no
// credential, and a process spawn per request would be paid for nothing.
func TestLocalBackendNeverTouchesTheKeyring(t *testing.T) {
	setup(t, "")
	calls := stubKeyring(t, "unused", true, true)
	if got := KeySourceFor("ollama"); got != "" {
		t.Fatalf("a local backend should have no key, got %q", got)
	}
	if len(calls.calls) != 0 {
		t.Fatalf("a local backend should not consult the keyring, called %v", calls.calls)
	}
}

func TestSaveKeyPrefersTheKeyring(t *testing.T) {
	setup(t, "an-old-file-key\n")
	stub := stubKeyring(t, "", true, true)

	if err := SaveKey("typed-into-the-panel"); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	// The plaintext copy goes away: a secret that is already in the keyring must not be left lying
	// in a config directory as well.
	if _, err := os.Stat(KeyFilePath()); !os.IsNotExist(err) {
		t.Fatalf("the key file should have been removed, stat gave %v", err)
	}
	// The save seeds the cache, so the settings panel's Test button does not wait for a spawn.
	if got := KeySourceFor("openai"); got != "keyring" {
		t.Fatalf("after a save the keyring should be the source, got %q", got)
	}
	if got := APIKeyFor("openai"); got != "typed-into-the-panel" {
		t.Fatalf("the saved key is not the one in force: %q", got)
	}

	// The secret has to reach the store: an unset Stdin would store an empty password and report
	// success, which is the failure that would only ever be noticed by the provider rejecting it.
	if cmd := stub.storeCmd(); cmd == nil {
		t.Fatalf("nothing stored the key: %v", stub.calls)
	} else if cmd.Stdin == nil {
		t.Fatal("the store was given no key on stdin")
	} else if got := stub.storedSecret(t); !strings.Contains(got, "typed-into-the-panel") {
		t.Fatalf("the store did not receive the key, it got %q", got)
	}

	// Both calls name the same item: the service, kind and attributes have to match, or a key is
	// written where nothing looks.
	var sawLabel bool
	for _, args := range stub.calls {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "service grammar-server") ||
			!strings.Contains(joined, "kind api-key") {
			t.Fatalf("the item identity is wrong: %v", args)
		}
		if args[0] == "store" && strings.Contains(joined, "--label") {
			sawLabel = true
		}
	}
	if !sawLabel {
		t.Fatalf("the item was stored without a label: %v", stub.calls)
	}
}

// A keyring that refuses must not lose the key: the file is the fallback, still 0600.
func TestSaveKeyFallsBackToTheFile(t *testing.T) {
	setup(t, "")
	stubKeyring(t, "", false, false)

	if err := SaveKey("typed-into-the-panel"); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	info, err := os.Stat(KeyFilePath())
	if err != nil {
		t.Fatalf("the key should have been written to the file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("the key file must be 0600, got %o", perm)
	}
	if got := KeySourceFor("openai"); got != "file" {
		t.Fatalf("the fallback key should report the file as its source, got %q", got)
	}
}
