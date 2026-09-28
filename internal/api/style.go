package api

import (
	"unicode/utf16"

	"grammar-server/internal/engine"
	"grammar-server/internal/lt"
)

// withStyleLints adds the deterministic style hints to the engine's findings.
//
// It does not sort. The check handler sorts the finished matches by offset, and
// that is the single place that must (harper's own order is not document order);
// sorting here as well was a second copy of the same invariant, waiting to drift.
//
// The hints come from internal/lt and never touch the engine: two tables plus two
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
	return out
}
