package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grammar-server/internal/engine"
)

// The dictionary file logic is where the bugs are: an entry defined two ways, a second click writing a
// second identical line, another person's hand-added word being dropped. None of that needs harper-ls.
// The mechanism itself — write the file, restart harper-ls, the word stops being flagged — is measured
// by TestUserDictionaryProbe and, end to end through this endpoint, by hand:
//
//	curl -s -X POST localhost:8875/v2/dictionary -H 'Content-Type: application/json' -d '{"word":"zorbulating"}'
//	curl -s -X POST localhost:8875/v2/check -H 'Content-Type: application/json' -d '{"text":"a zorbulating word"}'
//
// The second call reporting no matches is the proof; the endpoint's own `accepted` field is that same
// check, run by the endpoint on itself.

func TestListDictionaryDropsBlanksAndMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()

	if got := listDictionary(filepath.Join(dir, "nope.txt")); len(got) != 0 {
		t.Fatalf("a missing file must read as empty, got %v", got)
	}

	path := filepath.Join(dir, "dictionary.txt")
	if err := os.WriteFile(path, []byte("harper\n\n  spaced  \n\nzorbulating\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := listDictionary(path)
	want := []string{"harper", "spaced", "zorbulating"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAppendDictionaryIsIdempotentAndKeepsOtherLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dictionary.txt")
	if err := os.WriteFile(path, []byte("hand-written\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	added, err := appendDictionary(path, "zorbulating")
	if err != nil || !added {
		t.Fatalf("first append: added=%v err=%v", added, err)
	}
	// The same word again is a no-op, and so is a different lettercase of it: "Zorbulating" and
	// "zorbulating" are one word to a person, and two lines would read as a broken list.
	for _, again := range []string{"zorbulating", "Zorbulating", "  zorbulating  "} {
		added, err = appendDictionary(path, again)
		if err != nil {
			t.Fatal(err)
		}
		if added {
			t.Fatalf("appending %q twice must not add a line", again)
		}
	}
	body, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected the hand-written line plus one word, got %q", body)
	}
	if lines[0] != "hand-written" {
		t.Fatalf("a line a person wrote was reordered or lost: %q", body)
	}
}

func TestDictionaryRefusesAPhraseAndNonLoopbackWrites(t *testing.T) {
	// The loopback check runs before anything touches the file, and the phrase check before the engine is
	// involved, so both verdicts are testable without harper-ls running.
	s := &Server{}
	post := func(body, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v2/dictionary", strings.NewReader(body))
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		s.handleDictionary(w, req)
		return w
	}

	if w := post(`{"word":"two words"}`, "127.0.0.1:41234"); w.Code != 400 {
		t.Fatalf("a phrase must be refused with 400, got %d: %s", w.Code, w.Body)
	}
	if w := post(``, "127.0.0.1:41234"); w.Code != 400 {
		t.Fatalf("an empty word must be refused with 400, got %d", w.Code)
	}
	// A writer elsewhere would change what every other client sees, the same rule /v2/ignore has.
	if w := post(`{"word":"zorbulating"}`, "192.168.29.77:5555"); w.Code != 403 {
		t.Fatalf("a remote write must be refused with 403, got %d", w.Code)
	}
}

func TestCacheClearDropsEntriesAndKeepsCounters(t *testing.T) {
	// The dictionary write clears the cache; if clearing ever stopped working, a word taught after a text
	// was checked would stay flagged for that text, which is the bug this exists for.
	c := newLintCache(4)
	key := lintKey("fp", "text")
	c.put(key, []engine.Lint{{Rule: "SpellCheck"}})
	if _, ok := c.get(key); !ok {
		t.Fatal("a put entry must be gettable")
	}
	c.clear()
	if _, ok := c.get(key); ok {
		t.Fatal("clear must drop every entry")
	}
	if entries, _, _ := c.stats(); entries != 0 {
		t.Fatalf("entries after clear = %d, want 0", entries)
	}
}

func TestDictionaryGetReadsTheFileWithoutAnEngine(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/v2/dictionary", nil)
	w := httptest.NewRecorder()
	s.handleDictionary(w, req)

	if w.Code != 200 {
		t.Fatalf("GET must not need an engine, got %d", w.Code)
	}
	for _, field := range []string{"words", "count", "path"} {
		if !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Fatalf("the answer must carry %q: %s", field, w.Body)
		}
	}
}
