package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grammar-server/internal/engine"
)

// The ignore list is the honest fallback for a user's vocabulary — the engine still flags the word,
// this product stops repeating it — so the checks here are about the two ways it could be wrong: it
// could swallow more than the word it was told to, or it could not swallow that word at all.
func TestIgnoredWordsAreReadAsAList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ignored-words")
	if err := os.WriteFile(path, []byte("# my jargon\nZorbulating\n\n  kanban  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	words := readIgnored(path)
	if len(words) != 2 || !words["zorbulating"] || !words["kanban"] {
		t.Fatalf("the list should be two lower-cased words, got %v", words)
	}
	if readIgnored(filepath.Join(t.TempDir(), "absent")) == nil {
		t.Fatal("a missing file must be an empty list, not a nil one to trip over")
	}
}

func TestOnlyTheIgnoredWordIsSwallowed(t *testing.T) {
	text := "We are zorbulating the report today. Café hours are odd."
	words := readIgnored("") // empty list: nothing is ignored yet
	start := strings.Index(text, "zorbulating")
	if ignoredText(text, 0, 0, words) {
		t.Fatal("an empty list ignores nothing")
	}

	words["zorbulating"] = true
	if !ignoredText(text, start, start+11, words) {
		t.Fatal("the word itself must be ignored")
	}
	if !ignoredText(text, start-1, start+12, words) {
		t.Fatal("surrounding whitespace must not defeat the match — a finding can carry it")
	}
	// The whole sentence is not the word: a substring rule here would swallow every finding that
	// happened to contain an ignored word, which is the failure mode this filter must not have.
	if ignoredText(text, 0, len(text), words) {
		t.Fatal("a sentence-level finding must never be swallowed by a one-word entry")
	}
	// Offsets are UTF-16 code units, so a word past a multi-byte character has to land correctly.
	prefix := "We are zorbulating the report today. "
	cafe := len([]rune(prefix))
	words["café"] = true
	if !ignoredText(text, cafe, cafe+4, words) {
		t.Fatalf("a word after a multi-byte character must still match (offset %d)", cafe)
	}
}

func TestWritingTheIgnoreListKeepsEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "ignored-words")
	if err := writeIgnored(path, "Zorbulating", false); err != nil {
		t.Fatal(err)
	}
	if err := writeIgnored(path, "zorbulating", false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.ToLower(string(b)), "zorbulating"); got != 1 {
		t.Fatalf("a word added twice must be written once, got %d copies in %q", got, b)
	}
	if err := os.WriteFile(path, []byte("# mine\n"+string(b)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeIgnored(path, "kanban", false); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "# mine") {
		t.Fatalf("a comment a person wrote is not ours to lose: %q", b)
	}

	if err := writeIgnored(path, "ZORBULATING", true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(strings.ToLower(string(b)), "zorbulating") {
		t.Fatalf("forget must drop the line, whatever its case: %q", b)
	}
	// The comment is not a word, so it stays: an emptied list with a note in it is still a note.
	if err := writeIgnored(path, "kanban", true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(strings.ToLower(string(b)), "kanban") || !strings.Contains(string(b), "# mine") {
		t.Fatalf("forgetting the last word should leave just the comment: %q", b)
	}
	// With nothing left at all there is no file: one that lingers looks like something is still
	// ignored, and nothing reads a file to find nothing.
	fresh := filepath.Join(t.TempDir(), "ignored-words")
	if err := writeIgnored(fresh, "kanban", false); err != nil {
		t.Fatal(err)
	}
	if err := writeIgnored(fresh, "kanban", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("an empty list should leave no file behind, stat gave %v", err)
	}
}

// The endpoint: what it accepts, from where, and what it refuses.
func TestIgnoreEndpointRefusesWhatItShould(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := NewServer(nil)
	handler := s.Handler()

	call := func(method, body, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v2/ignore", strings.NewReader(body))
		if remote != "" {
			req.RemoteAddr = remote
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(http.MethodGet, "", "127.0.0.1:41234"); rec.Code != 405 {
		t.Errorf("GET /v2/ignore = %d, want 405", rec.Code)
	}
	// The list is the engine's: a word added over the LAN would change what everyone else sees.
	if rec := call(http.MethodPost, `{"word":"kanban"}`, "192.168.29.5:41234"); rec.Code != 403 {
		t.Errorf("a remote write = %d, want 403", rec.Code)
	}
	if rec := call(http.MethodPost, `{"word":"  "}`, "127.0.0.1:41234"); rec.Code != 400 {
		t.Errorf("an empty word = %d, want 400", rec.Code)
	}
	if rec := call(http.MethodPost, `not json`, "127.0.0.1:41234"); rec.Code != 400 {
		t.Errorf("junk = %d, want 400", rec.Code)
	}

	rec := call(http.MethodPost, `{"word":"kanban"}`, "127.0.0.1:41234")
	if rec.Code != 200 {
		t.Fatalf("adding from this machine = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var added struct {
		Ignored int    `json:"ignored"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.Ignored != 1 || !strings.HasSuffix(added.Path, "grammar-server/ignored-words") {
		t.Fatalf("the answer should carry the count and the file a client names in its toast: %+v", added)
	}
	if !readIgnored(added.Path)["kanban"] {
		t.Fatal("the word should be in the file it just reported")
	}

	rec = call(http.MethodPost, `{"word":"kanban","forget":true}`, "127.0.0.1:41234")
	if rec.Code != 200 {
		t.Fatalf("forget = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if len(readIgnored(ignoredWordsPath(t))) != 0 {
		t.Fatal("forget should have emptied the list")
	}
}

// ignoredWordsPath is the path this test's temp config dir resolves to, for assertions.
func ignoredWordsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "grammar-server", "ignored-words")
}

// The one that matters: a word added through the API stops being reported by /v2/check, and comes
// back when it is taken off the list. Real harper, real HTTP — the filter could be perfect and still
// be wired into a response nobody returns.
func TestIgnoredWordStopsBeingReported(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	url := localHarperServer(t)

	const word = "zorbulating"
	text := "We are " + word + " the report today."

	flagged := func() bool {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text, "language": "en-US"})
		resp, err := http.Post(url+"/v2/check", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Matches []struct {
				Offset int64 `json:"offset"`
				Length int64 `json:"length"`
			} `json:"matches"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Matches {
			if m.Offset == int64(strings.Index(text, word)) && m.Length == int64(len(word)) {
				return true
			}
		}
		return false
	}

	if !flagged() {
		t.Fatalf("VERDICT: %q is not flagged to begin with, so this test proves nothing", word)
	}
	body, _ := json.Marshal(map[string]string{"word": word})
	resp, err := http.Post(url+"/v2/ignore", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v2/ignore = %d", resp.StatusCode)
	}
	if flagged() {
		t.Fatalf("VERDICT: %q is still reported after the engine was told to ignore it", word)
	}

	body, _ = json.Marshal(map[string]any{"word": word, "forget": true})
	resp2, err := http.Post(url+"/v2/ignore", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if !flagged() {
		t.Fatalf("VERDICT: %q did not come back after being forgotten — the filter is not honest about its own state", word)
	}
}

// localHarperServer starts this package's own server over the real harper-ls, or skips. The other
// test files reach a server through their own package-level helpers; this one has to be self-contained
// because the helpers that check /v2/check live in the external test package, where the unexported
// functions under test here are not reachable.
func localHarperServer(t *testing.T) string {
	t.Helper()
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(NewServer(h).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}
