package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// Wordiness and passive voice are ours, not the engine's: harper reports nothing
// for these sentences and even LanguageTool's picky level only reports the
// passive one (as PASSIVE_VOICE_SIMPLE, category STYLE). They must therefore
// arrive as ordinary matches with LT rule ids, so existing clients render them
// with no changes.
func TestStyleHintsAppearAsMatches(t *testing.T) {
	srv := newTestServer(t)
	text := "In order to make a decision, the report was written by the team."

	matches, err := postCheck(srv.URL, text)
	if err != nil {
		t.Fatal(err)
	}
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
			"text": text, "language": "en-US", "disabledRules": []string{name},
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

// Offsets are UTF-16 code units, like every other match. A rune-offset slip
// shows up here: 😀 is one rune but two UTF-16 units, so the hint after it starts
// at 3, not 2.
func TestStyleHintOffsetsAreUTF16(t *testing.T) {
	srv := newTestServer(t)
	text := "😀 in order to run."

	matches, err := postCheck(srv.URL, text)
	if err != nil {
		t.Fatal(err)
	}
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
