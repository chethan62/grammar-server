package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestPhaseSplit (probe) reports where check time goes as documents grow:
// the didOpen→publishDiagnostics lint, and the per-diagnostic codeAction loop.
// Opt-in: HARPER_PROBE=1 go test -run TestPhaseSplit -v ./internal/engine/
//
// Gated because a 50 KB document takes minutes here (the per-finding codeAction
// loop is superlinear), which is fine for a measurement and fatal for `go test ./...`.
func TestPhaseSplit(t *testing.T) {
	if os.Getenv("HARPER_PROBE") == "" {
		t.Skip("measurement probe; set HARPER_PROBE=1 to run")
	}
	bin := os.Getenv("HARPER_BIN")
	if bin == "" {
		bin = "harper-ls"
	}
	h, err := NewHarper(bin, "American", nil)
	if err != nil {
		t.Skipf("no harper (%v)", err)
	}
	defer h.Close()

	dirty := "This is a sentance with a errror and teh wrong wurd in it. "
	clean := "The quick brown fox jumps over the lazy dog and keeps running today. "

	for _, kb := range []int{2, 10, 50} {
		for _, tc := range []struct{ name, sent string }{{"errors", dirty}, {"clean", clean}} {
			reps := kb * 1024 / len(tc.sent)
			text := strings.Repeat(tc.sent, reps)
			uri := h.newURI()

			t0 := time.Now()
			diags := h.checkDiags(text, uri)
			tLint := time.Since(t0)

			lints := h.diagsToLints(diags, text)
			t0 = time.Now()
			h.enrich(uri, diags, lints)
			tEnrich := time.Since(t0)

			reps_ := 0
			for _, l := range lints {
				reps_ += len(l.Replacements)
			}
			t.Logf("%2d KB %-6s lints=%3d  lint=%8.1fms  enrich=%9.1fms (%5.1fms/lint)  repl=%d",
				len(text)/1024, tc.name, len(lints),
				float64(tLint.Microseconds())/1000,
				float64(tEnrich.Microseconds())/1000,
				float64(tEnrich.Microseconds())/1000/float64(max(len(lints), 1)),
				reps_)
		}
	}
}
