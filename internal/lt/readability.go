package lt

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Stats is a deterministic delivery report: counts, sentence shape, and the
// three readability scores clients show as "the text reads at grade N".
//
// Everything here is derived from the text alone — no model, no dictionary, no
// network — so the numbers are cheap (<1 ms on a full document) and repeatable.
type Stats struct {
	Characters           int     `json:"characters"`
	Words                int     `json:"words"`
	Sentences            int     `json:"sentences"`
	Syllables            int     `json:"syllables"`
	UniqueWords          int     `json:"uniqueWords"`
	UniqueRatio          float64 `json:"uniqueRatio"`
	MeanSentenceWords    float64 `json:"meanSentenceWords"`
	MedianSentenceWords  float64 `json:"medianSentenceWords"`
	LongestSentenceWords int     `json:"longestSentenceWords"`
	ReadingTimeSeconds   int     `json:"readingTimeSeconds"`
	ReadingTime          string  `json:"readingTime"`
	FleschReadingEase    float64 `json:"fleschReadingEase"`
	FleschKincaidGrade   float64 `json:"fleschKincaidGrade"`
	GunningFog           float64 `json:"gunningFog"`
	Grade                string  `json:"grade"`
}

// Measure reports the stats for text. An empty text returns a zero Stats.
func Measure(text string) Stats {
	sentences := splitSentences(text)
	words := wordsOf(text)
	if len(words) == 0 {
		return Stats{}
	}

	sylls := 0
	unique := map[string]bool{}
	complex := 0 // words of 3+ syllables: Gunning Fog's "complex words"
	for _, w := range words {
		n := syllables(w)
		sylls += n
		if n >= 3 {
			complex++
		}
		unique[strings.ToLower(w)] = true
	}

	perSentence := make([]int, 0, len(sentences))
	longest := 0
	for _, s := range sentences {
		n := len(wordsOf(s))
		perSentence = append(perSentence, n)
		if n > longest {
			longest = n
		}
	}

	st := Stats{
		Characters:           len([]rune(text)),
		Words:                len(words),
		Sentences:            len(perSentence),
		Syllables:            sylls,
		UniqueWords:          len(unique),
		UniqueRatio:          round2(float64(len(unique)) / float64(len(words))),
		MeanSentenceWords:    round2(float64(len(words)) / float64(len(perSentence))),
		MedianSentenceWords:  round2(median(perSentence)),
		LongestSentenceWords: longest,
	}

	// Reading time at 230 wpm — the speed publishers quote for adult prose.
	st.ReadingTimeSeconds = int(float64(len(words)) / 230.0 * 60.0)
	st.ReadingTime = duration(st.ReadingTimeSeconds)

	wps := float64(len(words)) / float64(len(perSentence))
	spw := float64(sylls) / float64(len(words))
	st.FleschReadingEase = round2(clamp(206.835-1.015*wps-84.6*spw, 0, 100))
	st.FleschKincaidGrade = round2(max0(0.39*wps + 11.8*spw - 15.59))
	st.GunningFog = round2(max0(0.4 * (wps + 100*float64(complex)/float64(len(words)))))
	st.Grade = gradeBand(st.FleschKincaidGrade)
	return st
}

// splitSentences splits on ., ! and ?.
//
// ponytail: no abbreviation table, so "e.g." and "Dr." count as sentence ends
// and inflate the count slightly. That shows up as a fraction of a grade level,
// which is inside the tolerance of every score above — add an abbreviation list
// only if a UI ever shows sentence counts as a hard number.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' {
			if s := strings.TrimSpace(text[start : i+1]); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(text[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// wordsOf splits on anything that is not a letter, digit, apostrophe or hyphen,
// so "well-known" and "don't" stay one word.
func wordsOf(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '-'
	})
}

// syllables counts vowel groups with the usual corrections (silent final e, -le
// endings), and never returns less than 1.
//
// ponytail: this is a heuristic. It is wrong on roughly one word in ten, so it
// is fine for a banded grade level and must not be presented as an exact score.
// A dictionary would fix it at the cost of ~1 MB of data and a lookup per word.
func syllables(word string) int {
	w := strings.ToLower(strings.TrimFunc(word, func(r rune) bool {
		return !unicode.IsLetter(r)
	}))
	if w == "" {
		return 0
	}
	isVowel := func(r rune) bool { return strings.ContainsRune("aeiouy", r) }

	count, prevVowel := 0, false
	for _, r := range w {
		v := isVowel(r)
		if v && !prevVowel {
			count++
		}
		prevVowel = v
	}
	// Silent final "e": "make" is one syllable, but "-le" after a consonant
	// ("table", "little") is its own.
	if count > 1 && strings.HasSuffix(w, "e") && !strings.HasSuffix(w, "le") {
		count--
	}
	if count < 1 {
		count = 1
	}
	return count
}

func median(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := make([]int, len(xs))
	copy(s, xs)
	sort.Ints(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return float64(s[mid])
	}
	return (float64(s[mid-1]) + float64(s[mid])) / 2
}

func duration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%d sec", seconds)
	}
	return fmt.Sprintf("%d min %d sec", seconds/60, seconds%60)
}

// gradeBand names a Flesch–Kincaid grade the way style guides do, so a UI has a
// word to show and not just a number.
func gradeBand(grade float64) string {
	switch {
	case grade <= 1:
		return "Kindergarten"
	case grade <= 6:
		return "Elementary"
	case grade <= 9:
		return "Middle school"
	case grade <= 12:
		return "High school"
	case grade <= 16:
		return "College"
	default:
		return "Postgraduate"
	}
}

func clamp(v, lo, hi float64) float64 { return min(max(v, lo), hi) }

func max0(v float64) float64 { return max(v, 0) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }
