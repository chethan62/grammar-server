package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Pause state is the watcher's, and the engine only answers for it — so the bugs that matter are in
// reading the two files it writes, not in the HTTP surface. `readPaused` is where a real one lives: a
// paused-until the engine cannot parse must read as NOT paused, because the alternative is a corrupted
// or hand-edited file silencing the checker forever with no way to tell why.
//
// The live path (grammar-pause on, a suggestion stops appearing, grammar-pause off, it comes back) is the
// watcher's own suite; this is the parsing that sits between them.

func pauseRequest(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	// The handler does not touch the Server, so a zero value is the honest receiver here.
	(&Server{}).handlePause(rec, httptest.NewRequest(http.MethodGet, "/v2/pause", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/pause = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body.String())
	}
	return got
}

func TestPauseWithNothingWritten(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got := pauseRequest(t)
	if got["paused"] != false {
		t.Fatalf("no files at all must read as running, got %v", got["paused"])
	}
	// An empty list, not null: the window appends `blocked.join(", ")` and a null would print "null" in it.
	if list, ok := got["blocked"].([]any); !ok || len(list) != 0 {
		t.Fatalf("blocked must be an empty list, got %#v", got["blocked"])
	}
}

func TestPausedUntilIsAPointInTimeNotAFlag(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cache, "grammar-server"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "grammar-server", "paused-until")

	// The bytes grammar_core.write_pause writes: `"%d\n" % until`, a Unix timestamp in seconds. Written
	// here the same way rather than through a formatter of this test's own choosing, because the whole bug
	// this test now guards was the test and the code agreeing on a format the writer has never used.
	until := time.Now().Add(30 * time.Minute).Unix()
	if err := os.WriteFile(path, []byte(strconv.FormatInt(until, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := pauseRequest(t)
	if got["paused"] != true {
		t.Fatalf("a pause ending in the future must read as paused, got %v", got["paused"])
	}
	if want := time.Unix(until, 0).UTC().Format(time.RFC3339); got["until"] != want {
		t.Fatalf("until = %v, want %v", got["until"], want)
	}

	// The same file, once the time it names has gone by. This is the whole mechanism: the file is not
	// deleted when a pause expires, so a stale one must read as running or the checker never comes back.
	past := time.Now().Add(-30 * time.Minute).Unix()
	if err := os.WriteFile(path, []byte(strconv.FormatInt(past, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pauseRequest(t); got["paused"] != false {
		t.Fatalf("an expired pause must read as running, got %v", got["paused"])
	}
}

func TestUnreadablePauseFileDoesNotSilenceTheChecker(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cache, "grammar-server"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "grammar-server", "paused-until")

	// Neither of these names a moment in time. Reading either as "paused" would be a checker that stopped
	// answering because of a file nothing can fix from the window.
	for _, junk := range []string{"", "  \n", "true\n", "2026-13-45\n", "paused\n"} {
		if err := os.WriteFile(path, []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		if paused, until := readPaused(path); paused || until != "" {
			t.Fatalf("paused-until %q read as paused=%v until=%q, want running", junk, paused, until)
		}
	}

	// And a path that does not exist is the same answer as one that cannot be parsed.
	if paused, _ := readPaused(filepath.Join(cache, "grammar-server", "nope")); paused {
		t.Fatal("a missing paused-until must read as running")
	}
	if paused, _ := readPaused(""); paused {
		t.Fatal("an unresolvable path must read as running")
	}
}

func TestBlockedAppsIgnoresBlanksAndComments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// A missing file is no blocked applications, not an error.
	if got := listBlocked(filepath.Join(dir, "nope")); len(got) != 0 {
		t.Fatalf("a missing blocked-apps must read as empty, got %v", got)
	}

	path := filepath.Join(dir, "blocked-apps")
	body := "# a note to a reader\n\n  kate  \nsignal\n\n# trailing note\nkate\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := listBlocked(path)
	if len(got) != 2 || got[0] != "kate" || got[1] != "signal" {
		t.Fatalf("got %v, want [kate signal] — trimmed, deduped by sorting, comments dropped", got)
	}
}
