package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// checkResponse is the slice of the LanguageTool response these tests assert on.
type checkResponse struct {
	Matches []struct {
		Rule struct {
			ID        string `json:"id"`
			IssueType string `json:"issueType"`
			Category  struct {
				ID string `json:"id"`
			} `json:"category"`
		} `json:"rule"`
		Offset       int64 `json:"offset"`
		Length       int64 `json:"length"`
		Replacements []struct {
			Value string `json:"value"`
		} `json:"replacements"`
	} `json:"matches"`
	Warnings struct {
		IncompleteResults bool `json:"incompleteResults"`
	} `json:"warnings"`
}

// postCheckJSON sends a raw /v2/check body, so a test can use request parameters
// the postCheck helper does not cover.
func postCheckJSON(t *testing.T, url, body string) (*http.Response, checkResponse) {
	t.Helper()
	resp, err := http.Post(url+"/v2/check", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v2/check: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out checkResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode response %s: %v", raw, err)
		}
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v2/check: HTTP %d: %s", resp.StatusCode, raw)
	}
	return resp, out
}

// postCheckPicky asks for the style tier the way a writing UI does; the hints
// are opt-in (level=picky or enabledCategories=[STYLE]), as in LanguageTool.
func postCheckPicky(t *testing.T, url, text string) []checkMatch {
	t.Helper()
	body, err := json.Marshal(map[string]string{"text": text, "language": "en-US", "level": "picky"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+"/v2/check", "application/json", bytes.NewReader(body))
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
	return out.Matches
}

// withRule reports whether any match carries the rule id.
func withRule(out checkResponse, id string) int {
	n := 0
	for _, m := range out.Matches {
		if strings.EqualFold(m.Rule.ID, id) {
			n++
		}
	}
	return n
}

func withCategory(out checkResponse, id string) int {
	n := 0
	for _, m := range out.Matches {
		if strings.EqualFold(m.Rule.Category.ID, id) {
			n++
		}
	}
	return n
}

// A rule that is off by default can only fire if enabledRules reaches the
// engine's linter map: filtering results cannot invent a rule that never ran.
func TestEnabledRulesSwitchOnOffByDefaultRule(t *testing.T) {
	srv := newTestServer(t)

	_, base := postCheckJSON(t, srv.URL, `{"text":"I don't like it.","language":"en-US"}`)
	if n := withRule(base, "AvoidContractions"); n != 0 {
		t.Fatalf("AvoidContractions is off by default but fired %d time(s)", n)
	}

	_, on := postCheckJSON(t, srv.URL,
		`{"text":"I don't like it.","language":"en-US","enabledRules":["AvoidContractions"]}`)
	if n := withRule(on, "AvoidContractions"); n == 0 {
		t.Fatalf("enabledRules did not switch the rule on: %+v", on.Matches)
	}

	// Toggling on → off → on has to keep working. Every check opens a document in
	// harper-ls, and leaving those documents open made each later toggle re-lint
	// all of them: slowly at first, then past the diagnostics timeout, where the
	// check came back empty — a document reported clean because the server was
	// busy. This cycle is the regression guard for that.
	for i := 0; i < 3; i++ {
		_, again := postCheckJSON(t, srv.URL,
			`{"text":"I don't like it.","language":"en-US","enabledRules":["AvoidContractions"]}`)
		if withRule(again, "AvoidContractions") == 0 {
			t.Fatalf("cycle %d: the rule did not come back on", i)
		}
		_, reset := postCheckJSON(t, srv.URL, `{"text":"I don't like it.","language":"en-US"}`)
		if withRule(reset, "AvoidContractions") != 0 {
			t.Fatalf("cycle %d: the rule stayed on after being dropped", i)
		}
	}

	// enabledOnly narrows the run to just that rule: "teh" is a misspelling and
	// SpellCheck is not in the enabled set, so it must not be reported.
	_, only := postCheckJSON(t, srv.URL,
		`{"text":"I don't like teh it.","language":"en-US","enabledRules":["AvoidContractions"],"enabledOnly":true}`)
	if len(only.Matches) == 0 || len(only.Matches) != withRule(only, "AvoidContractions") {
		t.Fatalf("enabledOnly should report nothing but AvoidContractions, got %d match(es)", len(only.Matches))
	}
}

// Disabling by LanguageTool id has to reach the engine, not just the filter:
// otherwise harper still runs the rule and its cost is paid for nothing.
func TestDisabledRulesSwitchOffSpellCheck(t *testing.T) {
	srv := newTestServer(t)

	_, base := postCheckJSON(t, srv.URL, `{"text":"this has a misspeled wurd","language":"en-US"}`)
	if n := withCategory(base, "TYPOS"); n == 0 {
		t.Fatalf("expected a spelling match to disable, got %+v", base.Matches)
	}

	_, off := postCheckJSON(t, srv.URL,
		`{"text":"this has a misspeled wurd","language":"en-US","disabledRules":["MORFOLOGIK_RULE_EN_US"]}`)
	if n := withCategory(off, "TYPOS"); n != 0 {
		t.Fatalf("disabledRules did not switch spelling off, got %d match(es)", n)
	}
}

// Wordiness and passive voice are the style tier. LanguageTool reports its
// passive rule only at level=picky; ours is opt-in the same way, so editor
// clients are not shown style hints they never asked for.
func TestStyleTierIsOptIn(t *testing.T) {
	srv := newTestServer(t)
	const text = "In order to decide, the report was written by the team."

	_, base := postCheckJSON(t, srv.URL, `{"text":"`+text+`","language":"en-US"}`)
	if n := withCategory(base, "STYLE"); n != 0 {
		t.Fatalf("default level must stay the correctness tier, got %d style match(es)", n)
	}

	_, picky := postCheckJSON(t, srv.URL, `{"text":"`+text+`","language":"en-US","level":"picky"}`)
	if n := withRule(picky, "WORDINESS"); n == 0 {
		t.Fatalf("level=picky should report wordiness: %+v", picky.Matches)
	}
	if n := withRule(picky, "PASSIVE_VOICE_SIMPLE"); n == 0 {
		t.Fatalf("level=picky should report passive voice: %+v", picky.Matches)
	}

	// Asking for the category explicitly works the same way.
	_, byCat := postCheckJSON(t, srv.URL, `{"text":"`+text+`","language":"en-US","enabledCategories":["STYLE"]}`)
	if n := withCategory(byCat, "STYLE"); n == 0 {
		t.Fatal("enabledCategories=[STYLE] should switch the style tier on")
	}

	// …and disabledCategories takes it away again, even at picky.
	_, off := postCheckJSON(t, srv.URL,
		`{"text":"`+text+`","language":"en-US","level":"picky","disabledCategories":["STYLE"]}`)
	if n := withCategory(off, "STYLE"); n != 0 {
		t.Fatalf("disabledCategories=[STYLE] left %d style match(es)", n)
	}

	// enabledOnly with a category leaves only that category.
	_, onlyCat := postCheckJSON(t, srv.URL,
		`{"text":"The teh cat was written.","language":"en-US","level":"picky","enabledOnly":true,"enabledCategories":["STYLE"]}`)
	if len(onlyCat.Matches) == 0 || withCategory(onlyCat, "STYLE") != len(onlyCat.Matches) {
		t.Fatalf("enabledOnly with a category should report only it, got %+v", onlyCat.Matches)
	}
}

func TestUnknownLevelIsRefused(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"Fine.","language":"en-US","level":"verbose"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for level=verbose, got %d: %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(string(body), "Error: level must be") {
		t.Fatalf("expected a LanguageTool-shaped error body, got %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("LanguageTool error bodies are plain text, got %q", ct)
	}
}

// Clients read warnings.incompleteResults; a missing field reads as a broken
// response in strict ones.
func TestWarningsAndOrdering(t *testing.T) {
	srv := newTestServer(t)
	_, out := postCheckJSON(t, srv.URL,
		`{"text":"this has a misspeled wurd and teh.","language":"en-US"}`)
	if out.Warnings.IncompleteResults {
		t.Error("a full check must not report incompleteResults")
	}
	if len(out.Matches) < 2 {
		t.Fatalf("expected several matches, got %d", len(out.Matches))
	}
	for i := 1; i < len(out.Matches); i++ {
		if out.Matches[i].Offset < out.Matches[i-1].Offset {
			t.Fatalf("matches are not in document order: offset %d after %d",
				out.Matches[i].Offset, out.Matches[i-1].Offset)
		}
	}
}

// A document longer than one engine call is checked in chunks; every offset has
// to point at the same characters it would in a short document. A missed shift
// shows up as a match whose span is not the planted word.
func TestLongDocumentKeepsOffsets(t *testing.T) {
	srv := newTestServer(t)

	const filler = "The quick brown fox jumps over the lazy dog. "
	var b strings.Builder
	for i := 0; i < 1200; i++ { // ~54k characters: several 12k chunks
		b.WriteString(filler)
		if i == 10 || i == 600 || i == 1190 {
			b.WriteString("This line has teh typo. ")
		}
	}
	text := b.String()
	if len(text) < 3*12000 {
		t.Fatalf("test text is only %d bytes; it would not be chunked", len(text))
	}

	body, _ := json.Marshal(map[string]string{"text": text, "language": "en-US"})
	_, out := postCheckJSON(t, srv.URL, string(body))

	// ASCII-only text, so byte offsets equal UTF-16 offsets and the span can be
	// sliced directly.
	found := 0
	for _, m := range out.Matches {
		if m.Offset < 0 || m.Offset+m.Length > int64(len(text)) {
			t.Fatalf("match at %d+%d is outside the document (%d bytes)", m.Offset, m.Length, len(text))
		}
		if text[m.Offset:m.Offset+m.Length] == "teh" {
			found++
		}
	}
	if found != 3 {
		t.Fatalf("expected all 3 planted typos at their own offsets, found %d (%d matches total)",
			found, len(out.Matches))
	}
}

// Every response is sorted by offset, hints or no hints. harper's own order is not
// document order, and a style hint is appended after the engine's findings, so the
// list is genuinely out of order until the handler sorts it — this is the one place
// that does, now that withStyleLints no longer keeps a second copy of the invariant.
func TestMatchesAreOffsetSorted(t *testing.T) {
	srv := newTestServer(t)
	// The hint lands at offset 0 and the grammar finding at 26: appending puts them
	// in the wrong order, so a response that is sorted can only come from the sort.
	text := "In order to test this, he have a problem."

	resp, out := postCheckJSON(t, srv.URL, `{"text":"`+text+`","language":"en-US","level":"picky"}`)
	resp.Body.Close()
	if len(out.Matches) < 2 {
		t.Fatalf("want the hint and the grammar finding, got %+v", out.Matches)
	}
	if first := out.Matches[0]; first.Offset != 0 || first.Rule.ID != "WORDINESS" {
		t.Errorf("first match = %s at %d, want WORDINESS at 0: %+v", first.Rule.ID, first.Offset, out.Matches)
	}
	for i := 1; i < len(out.Matches); i++ {
		if out.Matches[i].Offset < out.Matches[i-1].Offset {
			t.Errorf("match %d at offset %d follows %d: %+v",
				i, out.Matches[i].Offset, out.Matches[i-1].Offset, out.Matches)
		}
	}
}

// enabledOnly is a promise to the client: "check nothing but this". When the things
// it named are not rules this engine has, harper answers zero matches and the client
// shows a clean document — the one answer this server must never invent (the
// language check refuses the same way for a code it cannot check). The field
// LanguageTool clients read for "this is not the whole story" is
// warnings.incompleteResults.
func TestEnabledOnlyThatCanMatchNothingSaysSo(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		name, body     string
		wantIncomplete bool
	}{
		{"a rule id nobody has", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledRules":["NoSuchRule"]}`, true},
		{"a harper rule harper does not ship", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledRules":["NoSuchHarperRule"]}`, true},
		{"a category we never emit", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledCategories":["NONSENSE"]}`, true},
		{"a category we do not emit but LT has", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledCategories":["PUNCTUATION"]}`, true},
		{"a rule that exists", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledRules":["MORFOLOGIK_RULE_EN_US"]}`, false},
		{"a harper rule by name", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledRules":["SpellCheck"]}`, false},
		{"a category we emit", `{"text":"teh report","language":"en-US","enabledOnly":true,"enabledCategories":["STYLE"]}`, false},
		{"enabledRules without enabledOnly", `{"text":"teh report","language":"en-US","enabledRules":["NoSuchRule"]}`, false},
		{"no filters at all", `{"text":"teh report","language":"en-US"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(srv.URL+"/v2/check", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var out struct {
				Matches  []checkMatch `json:"matches"`
				Warnings struct {
					IncompleteResults bool `json:"incompleteResults"`
				} `json:"warnings"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			if out.Warnings.IncompleteResults != tc.wantIncomplete {
				t.Errorf("incompleteResults = %v, want %v", out.Warnings.IncompleteResults, tc.wantIncomplete)
			}
			if tc.wantIncomplete && len(out.Matches) != 0 {
				t.Errorf("the check matched %d things while claiming to have checked nothing", len(out.Matches))
			}
		})
	}
}
