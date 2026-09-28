package lsp

import (
	"strings"
	"testing"
	"time"
)

// A peer that never answers must produce an error, not an eternal wait.
// `sleep` is the stub: it starts, reads nothing, writes nothing. Before the
// timeout existed this call blocked forever, and because the engine holds a
// mutex across a whole check cycle, one wedged request stalled every later one.
func TestRequestTimesOut(t *testing.T) {
	c, err := Start("sleep", "60")
	if err != nil {
		t.Fatalf("start stub: %v", err)
	}
	defer c.Stop()

	start := time.Now()
	_, err = c.RequestContext("textDocument/codeAction", map[string]any{}, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error, got nil (request hung)")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s, want ~300ms", d)
	}
}
