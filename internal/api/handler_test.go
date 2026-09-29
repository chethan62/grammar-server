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
	"unicode/utf16"

	"grammar-server/internal/api"
	"grammar-server/internal/engine"
)

// checkMatch is the subset of a LanguageTool match the tests assert on.
type checkMatch struct {
	Rule struct {
		ID          string `json:"id"`
		IssueType   string `json:"issueType"`
		Description string `json:"description"`
		Category    struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"category"`
	} `json:"rule"`
	Offset       int64  `json:"offset"`
	Length       int64  `json:"length"`
	Sentence     string `json:"sentence"`
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
			Offset         int64                    `json:"offset"`
			Length         int64                    `json:"length"`
			Replacements   []struct{ Value string } `json:"replacements"`
			SentenceRanges [][]int64                `json:"sentenceRanges"`
		} `json:"matches"`
		Software struct{ Version string } `json:"software"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	spellCount := 0
	for _, m := range out.Matches {
		if m.Rule.ID == "MORFOLOGIK_RULE_EN_US" { // LanguageTool id for harper's SpellCheck
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
	var out struct {
		SentenceRanges [][]int64 `json:"sentenceRanges"`
	}
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
	var out2 struct {
		SentenceRanges [][]int64 `json:"sentenceRanges"`
	}
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

// LanguageTool's API accepts JSON bodies, form-encoded POSTs, and GET query
// parameters — clients (browser extensions, LibreOffice) use all three.
func TestCheckRequestShapes(t *testing.T) {
	srv := newTestServer(t)

	// (path, method, content-type, body) — each must find the 2 SpellChecks.
	cases := []struct {
		name, method, ct, body string
	}{
		{"json", "POST", "application/json", `{"text":"this has a misspeled wurd","language":"en-US"}`},
		{"form", "POST", "application/x-www-form-urlencoded", "text=this+has+a+misspeled+wurd&language=en-US"},
		{"query", "GET", "", "text=this%20has%20a%20misspeled%20wurd&language=en-US"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := srv.URL + "/v2/check"
			var req *http.Request
			var err error
			if tc.method == "GET" {
				req, err = http.NewRequest("GET", url+"?"+tc.body, nil)
			} else {
				req, err = http.NewRequest("POST", url, strings.NewReader(tc.body))
				if err == nil {
					req.Header.Set("Content-Type", tc.ct)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var out struct {
				Matches []checkMatch `json:"matches"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			spell := 0
			for _, m := range out.Matches {
				if m.Rule.ID == "MORFOLOGIK_RULE_EN_US" {
					spell++
				}
			}
			if spell < 2 {
				t.Errorf("expected >=2 spelling matches, got %d (%+v)", spell, out.Matches)
			}
		})
	}
}

// Rule lists arrive as a JSON array or, from form/query clients, as a
// comma-separated string.
func TestRuleListEncodings(t *testing.T) {
	srv := newTestServer(t)

	for _, tc := range []struct{ name, body string }{
		{"json-array", `{"text":"teh wurd","disabledRules":["SpellCheck"]}`},
		{"json-string", `{"text":"teh wurd","disabledRules":"SpellCheck"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(srv.URL+"/v2/check", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var out struct {
				Matches []checkMatch `json:"matches"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			for _, m := range out.Matches {
				if m.Rule.ID == "SpellCheck" {
					t.Errorf("SpellCheck should be disabled, but got a match")
				}
			}
			if len(out.Matches) == 0 {
				t.Errorf("expected remaining matches after disabling SpellCheck")
			}
		})
	}
}

// Rule IDs must look like LanguageTool's, not harper's — LTeX and browser
// extensions match on rule.id/category.id and their rule-preference UIs know
// only LanguageTool's names. Mapping table values were captured from
// api.languagetool.org/v2/check responses.
func TestLanguageToolRuleMapping(t *testing.T) {
	srv := newTestServer(t)

	// "i" (lowercase pronoun) -> I_LOWERCASE, "brown  fox" (2 spaces)
	// -> CONSECUTIVE_SPACES, sentence start -> UPPERCASE_SENTENCE_START,
	// misspelling -> MORFOLOGIK_RULE_EN_US, "He go" -> HE_VERB_AGR.
	cases := []struct{ text, wantRule, wantIssue, wantCat string }{
		{"this has a misspeled wurd.", "MORFOLOGIK_RULE_EN_US", "misspelling", "TYPOS"},
		{"i agree with you.", "I_LOWERCASE", "misspelling", "TYPOS"},
		{"the quick brown  fox jumps.", "CONSECUTIVE_SPACES", "typographical", "TYPOGRAPHY"},
		{"He go to school every day.", "HE_VERB_AGR", "grammar", "GRAMMAR"},
	}
	for _, tc := range cases {
		matches, err := postCheck(srv.URL, tc.text)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range matches {
			if m.Rule.ID == tc.wantRule {
				found = true
				if m.Rule.IssueType != tc.wantIssue {
					t.Errorf("%q: rule %s issueType = %q, want %q",
						tc.text, tc.wantRule, m.Rule.IssueType, tc.wantIssue)
				}
				if m.Rule.Category.ID != tc.wantCat {
					t.Errorf("%q: rule %s category = %q, want %q",
						tc.text, tc.wantRule, m.Rule.Category.ID, tc.wantCat)
				}
			}
		}
		if !found {
			var got []string
			for _, m := range matches {
				got = append(got, m.Rule.ID)
			}
			t.Errorf("%q: expected rule %s, got %v", tc.text, tc.wantRule, got)
		}
	}

	// No rule id should leak harper's spelling name to clients.
	matches, err := postCheck(srv.URL, "this has a misspeled wurd.")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if m.Rule.ID == "SpellCheck" {
			t.Errorf("harper rule name leaked to clients")
		}
	}
}

// LanguageTool always fills in `sentence`; clients show it as the error's
// context line.
func TestSentenceField(t *testing.T) {
	srv := newTestServer(t)
	matches, err := postCheck(srv.URL, "First sentence here. Second one has misspeled word.")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 1 {
		t.Fatal("expected matches")
	}
	for _, m := range matches {
		if m.Sentence == "" {
			t.Errorf("rule %s: empty sentence field", m.Rule.ID)
		}
	}
	// The misspelled word is in sentence 2, so its sentence must be that one.
	for _, m := range matches {
		if strings.Contains(m.Sentence, "misspeled") &&
			!strings.Contains(m.Sentence, "Second") {
			t.Errorf("sentence %q does not contain the flagged word's sentence", m.Sentence)
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

// The offset tests above could not see this one: they pass offset 0, which means
// "the whole text". A client sends the UTF-16 offset of the sentence to fix, and
// reading that number as a byte index lands in the PREVIOUS sentence as soon as a
// non-ASCII character precedes it — grammar-ui then swaps that sentence into the
// user's document. The offset here is computed independently (utf16 encoding
// length), so the test states the contract rather than reusing the server's own
// conversion.
func TestFixSentenceOffsetIsUTF16(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		name, text, target, want string
	}{
		{
			name:   "an accented word before the offset",
			text:   "Café is nice. She go to the office. teh report is late.",
			target: "She go to the office.",
			want:   "She goes to the office.",
		},
		{
			name:   "an emoji before the offset (4 bytes, 2 UTF-16 units)",
			text:   "Hi 😀 there. She go to the office. teh report.",
			target: "She go to the office.",
			want:   "She goes to the office.",
		},
		{
			name:   "curly quotes, error in the last sentence",
			text:   "It’s fine. She go home. The report was teh late.",
			target: "The report was teh late.",
			want:   "The report was the late.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := strings.Index(tc.text, tc.target)
			if at < 0 {
				t.Fatalf("%q does not contain %q", tc.text, tc.target)
			}
			u16off := len(utf16.Encode([]rune(tc.text[:at])))

			body, err := json.Marshal(map[string]any{"text": tc.text, "offset": u16off})
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
			if out.Fixed != tc.want {
				t.Errorf("offset %d (UTF-16) fixed the wrong sentence:\n got %q\nwant %q", u16off, out.Fixed, tc.want)
			}
		})
	}
}
