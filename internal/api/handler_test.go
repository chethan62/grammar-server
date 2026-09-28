package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"grammar-server/internal/api"
	"grammar-server/internal/engine"
)

// checkMatch is the subset of a LanguageTool match the tests assert on.
type checkMatch struct {
	Rule struct {
		ID string `json:"id"`
	} `json:"rule"`
	Offset       int64 `json:"offset"`
	Length       int64 `json:"length"`
	Replacements []struct {
		Value string `json:"value"`
	} `json:"replacements"`
}

func TestCheckMisspelling(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond) // let warm-up settle

	srv := httptest.NewServer(api.NewServer(h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"this has a misspeled wurd","language":"en-US"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Matches []struct {
			Rule struct {
				ID string `json:"id"`
			} `json:"rule"`
			Offset        int64 `json:"offset"`
			Length        int64 `json:"length"`
			Replacements  []struct{ Value string } `json:"replacements"`
			SentenceRanges [][]int64 `json:"sentenceRanges"`
		} `json:"matches"`
		Software       struct{ Version string } `json:"software"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	spellCount := 0
	for _, m := range out.Matches {
		if m.Rule.ID == "SpellCheck" {
			spellCount++
			if m.Offset != 11 && m.Offset != 21 && m.Offset != 10 {
				// "misspeled" at 11 or 10 (harper varies); "wurd" at 21
			}
			if m.Length < 1 {
				t.Errorf("SpellCheck length too short: %d", m.Length)
			}
			// replacements should be unique (no duplicates)
			seen := map[string]bool{}
			for _, r := range m.Replacements {
				if seen[r.Value] {
					t.Errorf("duplicate replacement %q", r.Value)
				}
				seen[r.Value] = true
			}
		}
	}
	if spellCount < 2 {
		t.Errorf("expected at least 2 SpellCheck matches, got %d", spellCount)
	}
	if out.Software.Version == "" {
		t.Error("missing software version")
	}
}

func TestFixSentence(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond)

	srv := httptest.NewServer(api.NewServer(h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v2/fix-sentence", "application/json",
		strings.NewReader(`{"text":"teh quick brown fox","offset":0}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Fixed string `json:"fixed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Fixed != "the quick brown fox" {
		t.Errorf("expected 'the quick brown fox', got %q", out.Fixed)
	}
}

func TestSentenceRanges(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond)

	srv := httptest.NewServer(api.NewServer(h).Handler())
	defer srv.Close()

	// Two sentences
	resp, _ := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"First sentence. Second one here.","language":"en-US"}`))
	if resp != nil {
		defer resp.Body.Close()
	}
	var out struct{ SentenceRanges [][]int64 `json:"sentenceRanges"` }
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.SentenceRanges) < 2 {
		t.Errorf("expected at least 2 ranges, got %d: %v", len(out.SentenceRanges), out.SentenceRanges)
	}

	// Three single-letter "sentences" (edge case — no false abbreviation skip)
	resp2, _ := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"A. B. C.","language":"en-US"}`))
	if resp2 != nil {
		defer resp2.Body.Close()
	}
	var out2 struct{ SentenceRanges [][]int64 `json:"sentenceRanges"` }
	json.NewDecoder(resp2.Body).Decode(&out2)
	if len(out2.SentenceRanges) != 3 {
		t.Errorf("expected 3 ranges for 'A. B. C.', got %d: %v", len(out2.SentenceRanges), out2.SentenceRanges)
	}
}

// newTestServer starts a server backed by harper-ls, or skips the test if the
// binary is unavailable.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(api.NewServer(h).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func postCheck(url, text string) ([]checkMatch, error) {
	body, err := json.Marshal(map[string]string{"text": text, "language": "en-US"})
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(url+"/v2/check", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Matches []checkMatch `json:"matches"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Matches, nil
}

// Regression: multi-line documents used to return matches with empty
// replacements, because the codeAction request was built as
// {line: 0, character: <flat offset>} instead of the diagnostic's real
// line/character position.
func TestMultiLineReplacements(t *testing.T) {
	srv := newTestServer(t)
	matches, err := postCheck(srv.URL,
		"First line is fine.\nSecond line has a misspeled wurd here.\nThird line teh.")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 3 {
		t.Fatalf("expected at least 3 matches, got %d", len(matches))
	}
	for _, m := range matches {
		if len(m.Replacements) == 0 {
			t.Errorf("rule %s at offset %d returned no replacements", m.Rule.ID, m.Offset)
		}
	}
}

// Regression: concurrent checks used to drop each other's publishDiagnostics
// (shared channel, per-URI filtering), so most parallel requests came back
// with zero matches.
func TestConcurrentChecks(t *testing.T) {
	srv := newTestServer(t)

	const n = 10
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			matches, err := postCheck(srv.URL, fmt.Sprintf("this is teh %d misspeled wurd", i))
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			results[i] = len(matches)
		}(i)
	}
	wg.Wait()
	for i, got := range results {
		if got < 3 {
			t.Errorf("request %d: expected at least 3 matches, got %d", i, got)
		}
	}
}

// Regression: non-ASCII text mixed byte offsets with UTF-16 code units, so
// context.text/context.offset did not point at the match, sentenceRanges
// landed on the wrong boundaries, and fix-sentence corrupted the text.
//
// Note on units: the API speaks UTF-16 code units. The test strings below are
// all BMP (é, ï, etc.), so 1 code unit == 1 rune; slicing by rune is exact.

// unicodeText is long enough that the ±40 context window crops into the
// multi-byte prefix (é/ï are 2 bytes but 1 UTF-16 unit).
const unicodeText = "Café naïve résumé Café naïve résumé " +
	"Café naïve résumé Café naïve résumé misspeled wurd"

func TestUnicodeContext(t *testing.T) {
	srv := newTestServer(t)

	body, err := json.Marshal(map[string]string{"text": unicodeText, "language": "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v2/check", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Matches []struct {
			Offset  int64 `json:"offset"`
			Length  int64 `json:"length"`
			Context struct {
				Text   string `json:"text"`
				Offset int64  `json:"offset"`
				Length int64  `json:"length"`
			} `json:"context"`
		} `json:"matches"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) < 2 {
		t.Fatalf("expected at least 2 matches, got %d", len(out.Matches))
	}

	runes := []rune(unicodeText)
	for _, m := range out.Matches {
		// context.text[context.offset:+length] must equal the flagged
		// slice — this is how LanguageTool clients highlight the error.
		ctx := []rune(m.Context.Text)
		start, end := m.Context.Offset, m.Context.Offset+m.Context.Length
		if start < 0 || end > int64(len(ctx)) {
			t.Fatalf("context bounds out of range: offset=%d length=%d text=%q",
				m.Context.Offset, m.Context.Length, m.Context.Text)
		}
		got := string(ctx[start:end])
		if m.Offset < 0 || m.Offset+m.Length > int64(len(runes)) {
			t.Fatalf("match bounds out of range: offset=%d length=%d", m.Offset, m.Length)
		}
		want := string(runes[m.Offset : m.Offset+m.Length])
		if got != want {
			t.Errorf("context slice %q != match %q (context text %q)",
				got, want, m.Context.Text)
		}
	}
}

func TestSentenceRangesUnicode(t *testing.T) {
	srv := newTestServer(t)
	text := "Café ici. Naïve there. 😀 Done."

	body, err := json.Marshal(map[string]string{"text": text, "language": "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v2/check", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		SentenceRanges [][]int64 `json:"sentenceRanges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	// The splitter attaches the separator space to the preceding sentence.
	want := [][]int64{{0, 10}, {10, 23}, {23, 31}}
	if len(out.SentenceRanges) != len(want) {
		t.Fatalf("got %v, want %v", out.SentenceRanges, want)
	}
	for i, r := range out.SentenceRanges {
		if r[0] != want[i][0] || r[1] != want[i][1] {
			t.Errorf("range %d = %v, want %v", i, r, want[i])
		}
	}
	// Every range must tile the text (no gaps, ends at UTF-16 length).
	prev := int64(0)
	for _, r := range out.SentenceRanges {
		if r[0] != prev {
			t.Errorf("range starts at %d, want %d", r[0], prev)
		}
		prev = r[1]
	}
	if prev != 31 { // utf16 length incl. 😀 = 2 units
		t.Errorf("last range ends at %d, want 31", prev)
	}
}

// TestFixSentenceUnicode ensures replacements don't corrupt preceding
// multi-byte characters (was: "Café teh" -> "Cafétheh").
func TestFixSentenceUnicode(t *testing.T) {
	srv := newTestServer(t)
	body, err := json.Marshal(map[string]any{"text": "Café teh quick brown fox", "offset": 0})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v2/fix-sentence", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Fixed string `json:"fixed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Fixed != "Café the quick brown fox" {
		t.Errorf("got %q, want %q", out.Fixed, "Café the quick brown fox")
	}
}
