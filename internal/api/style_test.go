package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// Wordiness and passive voice are ours, not the engine's: harper reports nothing
// for these sentences and even LanguageTool's picky level only reports the
// passive one (as PASSIVE_VOICE_SIMPLE, category STYLE). They must therefore
// arrive as ordinary matches with LT rule ids, so existing clients render them
// with no changes. They are the picky tier — level=picky, or enabledCategories
// [STYLE] — so a client that asked for correctness alone never sees them.
func TestStyleHintsAppearAsMatches(t *testing.T) {
	srv := newTestServer(t)
	text := "In order to make a decision, the report was written by the team."

	matches := postCheckPicky(t, srv.URL, text)
	var wordy, passive []checkMatch
	for _, m := range matches {
		switch m.Rule.ID {
		case "WORDINESS":
			wordy = append(wordy, m)
		case "PASSIVE_VOICE_SIMPLE":
			passive = append(passive, m)
		}
	}
	// The engine reports nothing for this sentence, so every match here is ours.
	if len(matches) != 3 || len(wordy) != 2 || len(passive) != 1 {
		t.Fatalf("got %d matches (%d wordy, %d passive): %+v", len(matches), len(wordy), len(passive), matches)
	}
	for i := 1; i < len(matches); i++ {
		if matches[i].Offset < matches[i-1].Offset {
			t.Errorf("matches are out of offset order: %+v", matches)
		}
	}

	w := wordy[0]
	if w.Rule.IssueType != "style" || w.Rule.Category.ID != "STYLE" {
		t.Errorf("wordiness presentation = %+v", w.Rule)
	}
	// Sentence-initial "In order to" keeps its capital in the suggestion: "To ...",
	// not "to ...".
	if len(w.Replacements) != 1 || w.Replacements[0].Value != "To" {
		t.Errorf("wordiness replacements = %+v, want [To]", w.Replacements)
	}
	if w.Offset != 0 || w.Length != 11 {
		t.Errorf(`wordiness offset/length = %d/%d, want 0/11 ("in order to")`, w.Offset, w.Length)
	}
	if w.Sentence == "" {
		t.Error("a style hint must carry its sentence, like every other match")
	}
	if second := wordy[1]; second.Offset != 12 || second.Length != 15 || second.Replacements[0].Value != "decide" {
		t.Errorf(`second wordiness = %d/%d %+v, want "make a decision" at 12/15 -> decide`,
			second.Offset, second.Length, second.Replacements)
	}

	p := passive[0]
	// A hint, never a correction: a correct active-voice rewrite needs the actor,
	// and guessing one is how you ship wrong fixes.
	if len(p.Replacements) != 0 {
		t.Errorf("passive replacements = %+v, want none", p.Replacements)
	}
	if p.Offset <= w.Offset {
		t.Errorf("matches are out of order: wordiness at %d, passive at %d", w.Offset, p.Offset)
	}
}

// Hints are filtered by the same disabledRules mechanism as everything else, by
// either the LanguageTool id or our rule name.
func TestStyleHintsCanBeDisabled(t *testing.T) {
	srv := newTestServer(t)
	text := "In order to make a decision, the report was written by the team."

	// Each accepted spelling must suppress the match it names.
	for name, id := range map[string]string{
		"WORDINESS": "WORDINESS", "Wordiness": "WORDINESS",
		"PASSIVE_VOICE_SIMPLE": "PASSIVE_VOICE_SIMPLE", "PassiveVoice": "PASSIVE_VOICE_SIMPLE",
	} {
		body, err := json.Marshal(map[string]any{
			"text": text, "language": "en-US", "level": "picky",
			"disabledRules": []string{name},
		})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(srv.URL+"/v2/check", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Matches []checkMatch `json:"matches"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, m := range out.Matches {
			if m.Rule.ID == id {
				t.Errorf("disabledRules=%q still returned %s", name, id)
			}
		}
	}
}

// A preferred-term hint is our own machinery, so the two things that can break
// are the mapping to a LanguageTool id and the tier gate: STYLE under level=picky,
// invisible to a client that asked for correctness alone, and disable-able by
// either the LanguageTool id or our rule name.
func TestPreferredTermHint(t *testing.T) {
	srv := newTestServer(t)
	const text = "Please e-mail the report to me."

	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"default level hides it", `{"text":"` + text + `","language":"en-US"}`, 0},
		{"level=picky shows it", `{"text":"` + text + `","language":"en-US","level":"picky"}`, 1},
		{"disabled by id", `{"text":"` + text + `","language":"en-US","level":"picky","disabledRules":["PREFERRED_TERM"]}`, 0},
		{"disabled by name", `{"text":"` + text + `","language":"en-US","level":"picky","disabledRules":["PreferredTerm"]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, out := postCheckJSON(t, srv.URL, tc.body)
			resp.Body.Close()

			var count int
			var issueType, catID, repl string
			var offset, length int64
			for _, m := range out.Matches {
				if m.Rule.ID != "PREFERRED_TERM" {
					continue
				}
				count++
				issueType, catID = m.Rule.IssueType, m.Rule.Category.ID
				offset, length = m.Offset, m.Length
				if len(m.Replacements) > 0 {
					repl = m.Replacements[0].Value
				}
			}
			if count != tc.want {
				t.Fatalf("%d PREFERRED_TERM matches, want %d: %+v", count, tc.want, out.Matches)
			}
			if count == 0 {
				return
			}
			if issueType != "style" || catID != "STYLE" {
				t.Errorf("presentation = issueType %q category %q, want style / STYLE", issueType, catID)
			}
			// "Please e-mail ...": the hint covers just the form, not the sentence.
			if offset != 7 || length != 6 {
				t.Errorf(`offset/length = %d/%d, want 7/6 ("e-mail")`, offset, length)
			}
			if repl != "email" {
				t.Errorf("replacement = %q, want %q", repl, "email")
			}
		})
	}
}

// preferredVariants is LanguageTool's spelling-variant preference, and every JSON
// client sends it as an array. It was a plain string field, so ["en-GB"] 400'd with
// "cannot unmarshal array into Go struct field CheckRequest.preferredVariants of
// type string" — a request LanguageTool accepts. It now parses like the other list
// parameters and does something: the variant is the dialect.
func TestPreferredVariantsSetTheDialect(t *testing.T) {
	srv := newTestServer(t)

	for _, tc := range []struct {
		name, body, want string
		form             bool
	}{
		{name: "json array", body: `{"text":"Hello there.","language":"en-US","preferredVariants":["en-GB"]}`, want: "British"},
		{name: "json string", body: `{"text":"Hello there.","language":"en-US","preferredVariants":"en-GB"}`, want: "British"},
		{name: "form encoded", form: true, want: "British"},
		{name: "unknown variant is ignored", body: `{"text":"Hello there.","language":"en-US","preferredVariants":["fr-FR"]}`, want: "American"},
		{name: "language alone still decides", body: `{"text":"Hello there.","language":"en-GB"}`, want: "British"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp *http.Response
			if tc.form {
				r, err := http.PostForm(srv.URL+"/v2/check", url.Values{
					"text": {"Hello there."}, "language": {"en-US"}, "preferredVariants": {"en-GB"},
				})
				if err != nil {
					t.Fatal(err)
				}
				resp = r
			} else {
				r, _ := postCheckJSON(t, srv.URL, tc.body)
				resp = r
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("check returned %d, want 200", resp.StatusCode)
			}
			if got := dialectOf(t, srv.URL); got != tc.want {
				t.Errorf("engine dialect = %q, want %q", got, tc.want)
			}
		})
	}
}

func dialectOf(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Get(base + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Dialect string `json:"dialect"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Dialect == "" {
		t.Fatal("/status reports no dialect, so this test proves nothing")
	}
	return out.Dialect
}

// Offsets are UTF-16 code units, like every other match. A rune-offset slip
// shows up here: 😀 is one rune but two UTF-16 units, so the hint after it starts
// at 3, not 2.
func TestStyleHintOffsetsAreUTF16(t *testing.T) {
	srv := newTestServer(t)
	text := "😀 in order to run."

	matches := postCheckPicky(t, srv.URL, text)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want exactly the wordiness hint", matches)
	}
	if matches[0].Offset != 3 {
		t.Errorf("offset = %d, want 3 UTF-16 units (rune offset 2 or byte offset 5 are both wrong)",
			matches[0].Offset)
	}
	if matches[0].Length != 11 {
		t.Errorf("length = %d, want 11", matches[0].Length)
	}
}
