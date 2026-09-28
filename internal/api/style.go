package api

import (
	"sort"
	"unicode/utf16"

	"grammar-server/internal/engine"
	"grammar-server/internal/lt"
)

// withStyleLints adds the deterministic style hints to the engine's findings and
// returns everything in offset order. Clients render matches in the order they
// arrive, so an unsorted append would park every hint after the real errors.
//
// The hints come from internal/lt and never touch the engine: a table plus two
// regular expressions, microseconds, and nothing to fail.
func withStyleLints(text string, lints []engine.Lint) []engine.Lint {
	findings := lt.StyleFindings(text)
	if len(findings) == 0 {
		return lints
	}

	// Findings carry rune offsets; the API speaks UTF-16 code units (that is what
	// LanguageTool reports and what clients highlight with). One prefix pass makes
	// every offset O(1), and a possible invalid rune counts as one unit.
	runes := []rune(text)
	u16 := make([]int, len(runes)+1)
	for i, r := range runes {
		n := utf16.RuneLen(r)
		if n < 0 {
			n = 1
		}
		u16[i+1] = u16[i] + n
	}

	out := make([]engine.Lint, 0, len(lints)+len(findings))
	out = append(out, lints...)
	for _, f := range findings {
		if f.Start < 0 || f.End > len(runes) || f.Start >= f.End {
			continue // never send a malformed range to a client
		}
		out = append(out, engine.Lint{
			Rule:         f.Rule,
			Kind:         "style",
			Message:      f.Message,
			CharStart:    u16[f.Start],
			CharEnd:      u16[f.End],
			Replacements: f.Replacements,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CharStart < out[j].CharStart })
	return out
}
