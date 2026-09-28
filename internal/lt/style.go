package lt

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StyleFinding is a deterministic style hint: a wordy phrase worth cutting, or a
// passive construction. No engine and no model is involved — a table and two
// regular expressions — so the findings are instant, offline, and repeatable.
type StyleFinding struct {
	Rule         string   // "Wordiness" or "PassiveVoice"
	Message      string   // shown to the user as-is
	Replacements []string // the concise form; empty means "no suggestion"
	Start, End   int      // RUNE offsets into the text
}

// wordy maps a wordy phrase to the concise one. Keys are lower-case and matched
// at word boundaries, longest phrase first, so "due to the fact that" wins over
// "due to". This table IS the feature — extend it, don't build a parser.
//
// Only mechanically safe rewrites belong here, and only in the exact form
// written: "utilize" → "use" does not fire on "utilized" (the trailing boundary
// does not match), which keeps the suggestion from mangling inflections.
//
// LanguageTool has no rule for these (verified: even level=picky returns nothing
// for "in order to" / "due to the fact that" / "utilize") and harper only covers
// filler words, so nothing else reports them today.
var wordy = map[string]string{
	"in order to":                   "to",
	"due to the fact that":          "because",
	"in spite of the fact that":     "although",
	"notwithstanding the fact that": "although",
	"at this point in time":         "now",
	"at the present time":           "now",
	"in the event that":             "if",
	"in the event of":               "if",
	"in the near future":            "soon",
	"a large number of":             "many",
	"a majority of":                 "most",
	"has the ability to":            "can",
	"have the ability to":           "can",
	"is able to":                    "can",
	"are able to":                   "can",
	"with regard to":                "about",
	"prior to":                      "before",
	"subsequent to":                 "after",
	"on a daily basis":              "daily",
	"in a timely manner":            "promptly",
	"in the absence of":             "without",
	"for the purpose of":            "to",
	"by means of":                   "by",
	"take into consideration":       "consider",
	"there is a need for":           "we need",
	"make a decision":               "decide",
	"utilize":                       "use",
	"utilise":                       "use",
	"ascertain":                     "find out",
	"commence":                      "start",
	"terminate":                     "end",
	"endeavour":                     "try",
	"endeavor":                      "try",
}

// wordyRe alternates every phrase, longest first (Go's regexp takes the first
// alternative that matches, not the longest), case-insensitively, at word
// boundaries.
var wordyRe = func() *regexp.Regexp {
	keys := make([]string, 0, len(wordy))
	for k := range wordy {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		quoted = append(quoted, regexp.QuoteMeta(k))
	}
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(quoted, "|") + `)\b`)
}()

// passiveRe matches "was written", "is being reviewed", "were made".
//
// ponytail: no part-of-speech tagger and no dependency. Regular past participles
// come from the -ed suffix, irregulars from an explicit list, so it misses
// irregulars outside the list and fires on stative uses ("is located", "was
// born") — expect roughly 15% false positives. That is why this is a HINT with
// no replacement, never a correction.
var passiveRe = regexp.MustCompile(`(?i)\b(?:is|are|was|were|be|been|being)\s+(?:\w+ed|` +
	`written|made|given|taken|seen|done|held|sent|built|found|shown|told|left|kept|put|set|cut|` +
	`read|caught|brought|thought|bought|taught|sought|known|driven|chosen|spoken|broken|drawn|` +
	`thrown|grown|begun|won|lost|paid|meant|met|felt|heard)\b`)

// StyleFindings returns the style hints in text, ordered by offset.
func StyleFindings(text string) []StyleFinding {
	out := make([]StyleFinding, 0, 4)

	for _, m := range wordyRe.FindAllStringIndex(text, -1) {
		phrase := text[m[0]:m[1]]
		repl, ok := wordy[strings.ToLower(phrase)]
		if !ok {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(phrase); unicode.IsUpper(r) && repl != "" {
			repl = strings.ToUpper(repl[:1]) + repl[1:]
		}
		out = append(out, StyleFinding{
			Rule:         "Wordiness",
			Message:      fmt.Sprintf("Wordy phrase %q: consider %q", phrase, repl),
			Replacements: []string{repl},
			Start:        utf8.RuneCountInString(text[:m[0]]),
			End:          utf8.RuneCountInString(text[:m[1]]),
		})
	}

	for _, m := range passiveRe.FindAllStringIndex(text, -1) {
		out = append(out, StyleFinding{
			Rule:    "PassiveVoice",
			Message: "Passive voice: consider naming the actor and using the active voice.",
			Start:   utf8.RuneCountInString(text[:m[0]]),
			End:     utf8.RuneCountInString(text[:m[1]]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}
