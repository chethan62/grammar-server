package lt

import (
	"math"
	"strings"
	"testing"
)

// Exact counts for one short text: these are the numbers a UI shows, so they
// are pinned rather than approximated.
func TestMeasureCounts(t *testing.T) {
	text := "The cat sat. Do you not agree? Yes indeed, the well-known cat sat on the mat!"
	st := Measure(text)

	if st.Words != 16 {
		t.Errorf("Words = %d, want 16", st.Words)
	}
	if st.Sentences != 3 {
		t.Errorf("Sentences = %d, want 3", st.Sentences)
	}
	if st.LongestSentenceWords != 9 {
		t.Errorf("LongestSentenceWords = %d, want 9", st.LongestSentenceWords)
	}
	if st.MedianSentenceWords != 4 {
		t.Errorf("MedianSentenceWords = %v, want 4 (mean of 4 and 4 from [3 4 9])", st.MedianSentenceWords)
	}
	if st.MeanSentenceWords != 5.33 {
		t.Errorf("MeanSentenceWords = %v, want 5.33", st.MeanSentenceWords)
	}
	if st.UniqueWords != 12 {
		t.Errorf("UniqueWords = %d, want 12", st.UniqueWords)
	}
	if st.UniqueRatio != 0.75 {
		t.Errorf("UniqueRatio = %v, want 0.75", st.UniqueRatio)
	}
	if st.ReadingTime != "4 sec" {
		t.Errorf("ReadingTime = %q, want %q", st.ReadingTime, "4 sec")
	}
	if st.Characters != len([]rune(text)) {
		t.Errorf("Characters = %d, want the rune count %d", st.Characters, len([]rune(text)))
	}
	// Rune counting, not byte counting: "Café" is 4 characters, 5 bytes.
	if got := Measure("Café").Characters; got != 4 {
		t.Errorf(`Measure("Café").Characters = %d, want 4`, got)
	}
}

func TestSyllables(t *testing.T) {
	cases := map[string]int{
		"cat": 1, "beautiful": 3, "make": 1, "table": 2, "the": 1,
		"rhythm": 1, "a": 1, "banana": 3, "note": 1, "well-known": 2,
		"": 0, "-": 0,
	}
	for word, want := range cases {
		if got := syllables(word); got != want {
			t.Errorf("syllables(%q) = %d, want %d", word, got, want)
		}
	}
}

// The three scores are only useful as a banded signal, so the test pins the
// properties a UI depends on: bounded output, no NaN on empty input, and a
// harder text scoring harder than a simple one.
func TestScoresAreBoundedAndOrdered(t *testing.T) {
	simple := Measure("The cat sat on the mat. The dog ran fast.")
	hard := Measure("The implementation of comprehensive internationalisation strategies " +
		"necessitates considerable infrastructural reorganisation across multiple organisational subdivisions.")

	if simple.FleschReadingEase < 0 || simple.FleschReadingEase > 100 {
		t.Errorf("FleschReadingEase = %v, want 0..100", simple.FleschReadingEase)
	}
	if simple.FleschReadingEase <= hard.FleschReadingEase {
		t.Errorf("simple text scored %v, hard text %v: the hard text must be less readable",
			simple.FleschReadingEase, hard.FleschReadingEase)
	}
	if hard.FleschKincaidGrade <= simple.FleschKincaidGrade {
		t.Errorf("grade = %v vs %v: the hard text must score a higher grade",
			hard.FleschKincaidGrade, simple.FleschKincaidGrade)
	}
	if hard.GunningFog <= simple.GunningFog {
		t.Errorf("fog = %v vs %v: the hard text must score higher fog",
			hard.GunningFog, simple.GunningFog)
	}
	if simple.Grade == "" {
		t.Error("Grade band is empty")
	}

	for _, empty := range []string{"", "   ", "!!!", "..."} {
		st := Measure(empty)
		if st.Words != 0 || st.Sentences != 0 {
			t.Errorf("Measure(%q) = words %d, sentences %d; want zero", empty, st.Words, st.Sentences)
		}
		if math.IsNaN(st.FleschReadingEase) || math.IsNaN(st.FleschKincaidGrade) {
			t.Errorf("Measure(%q) produced NaN", empty)
		}
	}
}

// 230 wpm is the number the reading time is quoted at; pin it so a change to
// the constant fails loudly instead of silently re-scaling the UI.
func TestReadingTimeUsesTheQuotedWPM(t *testing.T) {
	st := Measure(strings.TrimSpace(strings.Repeat("word ", 230)))
	if st.Words != 230 {
		t.Fatalf("Words = %d, want 230", st.Words)
	}
	if st.ReadingTimeSeconds != 60 {
		t.Errorf("230 words = %d seconds, want 60 (230 wpm)", st.ReadingTimeSeconds)
	}
	if st.ReadingTime != "1 min 0 sec" {
		t.Errorf("ReadingTime = %q, want %q", st.ReadingTime, "1 min 0 sec")
	}
}
