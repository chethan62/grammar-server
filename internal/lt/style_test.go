package lt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Every entry in the table must fire, must be replaced by its concise form, and
// must not be broken by the case it appears in. A dead entry is worse than a
// missing one: it reads like coverage in the source and does nothing.
func TestEveryWordyEntryFires(t *testing.T) {
	for phrase, want := range wordy {
		text := "We should " + phrase + " do it."
		findings := StyleFindings(text)
		if len(findings) != 1 {
			t.Errorf("%q: %d findings, want exactly 1", phrase, len(findings))
			continue
		}
		f := findings[0]
		if f.Rule != "Wordiness" {
			t.Errorf("%q: rule = %q", phrase, f.Rule)
		}
		if got := runeSlice(text, f); got != phrase {
			t.Errorf("%q: covered %q", phrase, got)
		}
		if len(f.Replacements) != 1 || f.Replacements[0] != want {
			t.Errorf("%q: replacements = %v, want [%q]", phrase, f.Replacements, want)
		}
	}
}

// Uppercase phrasing keeps its capital: "In order to" → "To", not "to".
func TestCaseIsPreserved(t *testing.T) {
	f := StyleFindings("In order to ship this, we hurry.")
	if len(f) != 1 || f[0].Replacements[0] != "To" {
		t.Fatalf("findings = %+v, want one with replacement %q", f, "To")
	}
}

// Inflected forms are deliberately left alone: "utilized" must not become
// "use" (which would mangle the sentence). The trailing word boundary is what
// makes this safe, so it is pinned here.
func TestInflectionsAreNotTouched(t *testing.T) {
	for _, text := range []string{
		"we utilized the car",
		"they commenced the work",
		"the termination was quick",
		"she was ascertaining the facts",
	} {
		if f := StyleFindings(text); len(f) != 0 {
			t.Errorf("%q: %+v, want no findings", text, f)
		}
	}
}

func TestPassiveVoice(t *testing.T) {
	hits := map[string]string{
		"The report was written by the team.": "was written",
		"The cakes were made yesterday.":      "were made",
		"It is being reviewed right now.":     "being reviewed",
	}
	for text, want := range hits {
		f := StyleFindings(text)
		if len(f) != 1 {
			t.Errorf("%q: %d findings, want 1 (%+v)", text, len(f), f)
			continue
		}
		if f[0].Rule != "PassiveVoice" {
			t.Errorf("%q: rule = %q", text, f[0].Rule)
		}
		if got := runeSlice(text, f[0]); got != want {
			t.Errorf("%q: covered %q, want %q", text, got, want)
		}
		if len(f[0].Replacements) != 0 {
			t.Errorf("%q: a hint must not carry a replacement, got %v", text, f[0].Replacements)
		}
	}

	active := []string{
		"The team wrote the report.",
		"She is happy about the outcome.",
		"The dog ran fast and the cat slept.",
		"He has been to Paris.",
	}
	for _, text := range active {
		if f := StyleFindings(text); len(f) != 0 {
			t.Errorf("%q: %+v, want no passive hint", text, f)
		}
	}
}

// Offsets are rune offsets, so multi-byte text does not shift them: this is the
// bug class the UTF-16 conversions in internal/api exist for.
func TestOffsetsAreRuneOffsets(t *testing.T) {
	text := "Café ☕ in order to run, and it was written by hand."
	f := StyleFindings(text)
	if len(f) != 2 {
		t.Fatalf("findings = %+v, want 2", f)
	}
	if got := runeSlice(text, f[0]); got != "in order to" {
		t.Errorf("first finding covers %q", got)
	}
	if got := runeSlice(text, f[1]); got != "was written" {
		t.Errorf("second finding covers %q", got)
	}
	if f[0].Start >= f[1].Start {
		t.Errorf("findings are not in offset order: %+v", f)
	}
}

func runeSlice(text string, f StyleFinding) string {
	r := []rune(text)
	if f.Start < 0 || f.End > len(r) {
		return "<out of range>"
	}
	return string(r[f.Start:f.End])
}

// The replacement must actually read as a replacement where it lands.
func TestReplacementOverlaysTheFinding(t *testing.T) {
	text := "Due to the fact that it rained, we stayed in."
	f := StyleFindings(text)
	if len(f) != 1 {
		t.Fatalf("findings = %+v", f)
	}
	r := []rune(text)
	fixed := string(r[:f[0].Start]) + f[0].Replacements[0] + string(r[f[0].End:])
	if !strings.HasPrefix(fixed, "Because it rained") {
		t.Errorf("fixed = %q, want it to start with %q", fixed, "Because it rained")
	}
	if utf8.RuneCountInString("Due to the fact that") != f[0].End-f[0].Start {
		t.Errorf("finding spans %d runes, want 20", f[0].End-f[0].Start)
	}
}
