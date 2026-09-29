package lt

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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
		if n == 0 {
			continue // a punctuation-only fragment ("...") is not a sentence
		}
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

// splitSentences splits text into sentences for the counts and scores above.
//
// Not every period is a sentence end, and the counts are shown as hard numbers by
// the UI (and feed every score above), so the four rules below are the ones that
// matter in real prose: "Dr. Smith" is one sentence, "1.5 lakh" is one, a URL or a
// file name is one, and an ellipsis is not a sentence end at all.
//
// The period after an abbreviation is the one case that needs data, so there is a
// table — the size that used to be deferred until a UI showed sentence counts:
// that UI is /v2/stats plus grammar-ui, so it is here.
func splitSentences(text string) []string {
	ranges := SentenceRanges(text)
	out := make([]string, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, text[r[0]:r[1]])
	}
	return out
}

// SentenceRanges returns the byte range of every sentence in text, trimmed of
// surrounding whitespace. It is the definition of "a sentence" for this codebase:
// the stats counts, the `sentence`/`sentenceRanges` response fields and the range
// /v2/fix-sentence reports all come from it. Two implementations of that boundary
// is how a client ends up replacing different text than the server fixed.
func SentenceRanges(text string) [][2]int {
	var out [][2]int
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		if c == '.' && !endsSentence(text, i) {
			continue
		}
		if r, ok := trimRange(text, start, i+1); ok {
			out = append(out, r)
		}
		start = i + 1
	}
	if r, ok := trimRange(text, start, len(text)); ok {
		out = append(out, r)
	}
	return out
}

// trimRange trims unicode whitespace off both ends of text[start:end] and reports
// whether anything is left.
func trimRange(text string, start, end int) ([2]int, bool) {
	for start < end {
		r, size := utf8.DecodeRuneInString(text[start:])
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	if start >= end {
		return [2]int{}, false
	}
	return [2]int{start, end}, true
}

// abbreviations always sit inside a sentence: after a title or a latin
// abbreviation, prose never starts a new sentence ("Mr. Smith", "e.g. this").
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true,
	"jr": true, "st": true, "vs": true, "eg": true, "ie": true, "no": true,
	"fig": true, "vol": true, "approx": true, "dept": true, "univ": true,
	"est": true, "misc": true, "pvt": true,
}

// abbreviationsThatCanEnd is the other half: "It works at Acme Inc." is a sentence
// and "Jan." is not, so for these the dot ends a sentence only when a capital
// follows ("Inc. They ship weekly." vs "Inc. and its staff"). A digit means the
// abbreviation is attached to a number — "Jan. 5" — never a new sentence.
var abbreviationsThatCanEnd = map[string]bool{
	"etc": true, "inc": true, "ltd": true, "co": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true, "jul": true,
	"aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true,
}

// endsSentence reports whether the '.' at i ends a sentence.
func endsSentence(text string, i int) bool {
	// Part of a run of dots: "..." is not three sentence ends. The last dot is
	// rejected here too, and the sentence it sits in ends at the next real one.
	if i > 0 && text[i-1] == '.' {
		return false
	}
	// A period with no whitespace after it is inside a token, not after a
	// sentence: example.com, report.txt, a.b. Closing punctuation may sit
	// between the dot and the space.
	j := i + 1
	for j < len(text) {
		r, size := utf8.DecodeRuneInString(text[j:])
		if !strings.ContainsRune(")]}\"'”’", r) {
			break
		}
		j += size
	}
	if j < len(text) {
		if r, _ := utf8.DecodeRuneInString(text[j:]); !unicode.IsSpace(r) {
			return false
		}
	}
	// An abbreviation or an initial: "Dr. Smith", "R. K. Narayan". A period after
	// a number is not one ("It grew to 5.") — that word is not in the table.
	word := wordBefore(text, i)
	switch {
	case abbreviations[word]:
		return false
	case abbreviationsThatCanEnd[word]:
		return nextStartsUpper(text, j)
	}
	return len(word) != 1
}

// nextStartsUpper reports whether the next letter after from is a capital. A digit
// first (the 5 in "Jan. 5") is not a new sentence.
func nextStartsUpper(text string, from int) bool {
	for i := from; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsLetter(r) {
			return unicode.IsUpper(r)
		}
		if unicode.IsDigit(r) {
			return false
		}
		i += size
	}
	return false
}

// wordBefore is the run of letters ending just before i, lowercased.
func wordBefore(text string, i int) string {
	start := i
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if !unicode.IsLetter(r) {
			break
		}
		start -= size
	}
	return strings.ToLower(text[start:i])
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
