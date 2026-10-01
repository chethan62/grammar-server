package engine

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUserDictionaryProbe asks the two questions that decide whether "Add to dictionary" and "ignore
// this rule" can be built against this engine at all.
//
// Why it is a probe and not a feature: references/harper-dictionaries.md records that `userDictPath`
// is dead over LSP in 2.11.0 — three mechanisms measured, including initializationOptions — and that
// the route harper implements itself had never been tried: the codeActions against the lint whose
// commands write harper's *own* dictionaries (`HarperAddToUserDict`, `...ToWSDict`, `...ToFileDict`)
// or ignore it (`HarperIgnoreLint`). A dictionary that accepts a word and ignores it is worse than no
// dictionary, so this measures before anything is built on top.
//
//	HARPER_PROBE=1 go test -run TestUserDictionaryProbe -v ./internal/engine/
//
// Every line is labelled with what it means, including the failure shapes, because the lesson
// recorded in that reference is that a check which cannot fail correctly looks exactly like success.
// This probe's first run proved the point against itself: it executed the file-dictionary command
// with a null argument list, read harper's refusal as "the route is dead", and printed a verdict
// about the probe rather than about harper.
//
// XDG_CONFIG_HOME points at a temporary directory before harper-ls starts, so the language server
// writes its own files there instead of into the ones this machine actually uses.
func TestUserDictionaryProbe(t *testing.T) {
	if os.Getenv("HARPER_PROBE") == "" {
		t.Skip("dictionary probe; set HARPER_PROBE=1 to run")
	}
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)

	bin := os.Getenv("HARPER_BIN")
	if bin == "" {
		bin = "harper-ls"
	}
	h, err := NewHarper(bin, "American", nil)
	if err != nil {
		t.Skipf("no harper (%v)", err)
	}
	defer h.Close()

	const word = "zorbulating"
	text := "We are " + word + " the report today.\n"

	open := probeLints(t, h, text)
	for _, l := range open {
		t.Logf("finding while open: rule=%s kind=%s [%d,%d) %q", l.Rule, l.Kind, l.CharStart, l.CharEnd, l.Message)
	}
	spelling := pickSpelling(open, text, word)
	if spelling == nil {
		t.Fatalf("VERDICT: nothing in the lint list covers %q, so there is nothing to ask about", word)
	}
	t.Logf("asking about: rule=%s kind=%s", spelling.Rule, spelling.Kind)

	// The same request enrich() makes, so the answer is the one production would get. The document
	// has to be open for harper to answer about its range, so the diagnostics are not discarded —
	// they are simply not needed here beyond opening it.
	uri := h.newURI()
	if _, err := h.checkDiags(text, uri); err != nil {
		t.Fatalf("checkDiags: %v", err)
	}
	resp, err := h.c.Request("textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"range":        spelling.rangeMap(),
		"context":      map[string]any{"diagnostics": []any{spelling.diagMap()}},
	})
	if err != nil {
		t.Fatalf("codeAction: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("VERDICT: codeAction answered something that is not a list (%v): %s", err, resp.Result)
	}
	t.Logf("actions offered for that finding: %d", len(items))

	// The dictionary commands come back as bare names with no arguments, while harper's own
	// record-lint action carries the payload the server expects: a serialized Lint. That payload is
	// the template — handing these commands nothing is answered with
	// "invalid type: null, expected a sequence", which is what the first run mistook for a dead end.
	var args []any
	for i, it := range items {
		title, _ := it["title"].(string)
		kind, _ := it["kind"].(string)
		_, hasEdit := it["edit"]
		command, commandArgs := commandOf(it)
		if len(commandArgs) > 0 && args == nil {
			args = commandArgs
		}
		t.Logf("  [%d] title=%q kind=%q edit=%v command=%q args=%d",
			i, title, kind, hasEdit, command, len(commandArgs))
	}
	if args == nil {
		t.Logf("VERDICT: no action carries arguments, so there is no template for what these commands " +
			"expect; asking harper for one is the next step, not guessing.")
		return
	}
	t.Logf("payload for the commands: %s", truncate(string(mustJSON(args)), 300))

	// 1. the user dictionary — the feature that motivated all of this.
	if !execCommand(t, h, "HarperAddToUserDict", args) {
		t.Logf("VERDICT: the user-dictionary command refuses a payload we can build; not shippable as is")
	} else {
		immediate := probeLints(t, h, text)
		t.Logf("immediately after adding to the user dictionary: %q still flagged? %v",
			word, pickSpelling(immediate, text, word) != nil)

		// A dictionary is read when the process starts (measured earlier), so a restart is the honest
		// second half of the question — and the cost per word a feature would have to pay.
		if err := h.Reconnect(); err != nil {
			t.Fatalf("reconnect: %v", err)
		}
		afterRestart := probeLints(t, h, text)
		stillFlagged := pickSpelling(afterRestart, text, word) != nil
		t.Logf("after a restart of harper-ls: %q still flagged? %v", word, stillFlagged)
		if !stillFlagged {
			t.Logf("VERDICT: WORKS — harper's own command adds the word and honours it after a restart")
		} else {
			t.Logf("VERDICT: the command was accepted but the word is still flagged — do not ship it")
		}
	}

	// 2. the ignore command, which that same reference asked to be tested the same way before any
	// ignore feature is built.
	if execCommand(t, h, "HarperIgnoreLint", args) {
		ignored := probeLints(t, h, text)
		t.Logf("after HarperIgnoreLint: the finding is gone? %v", pickSpelling(ignored, text, word) == nil)
		if err := h.Reconnect(); err == nil {
			afterRestart := probeLints(t, h, text)
			t.Logf("after a restart: still gone? %v", pickSpelling(afterRestart, text, word) == nil)
		}
	} else {
		t.Logf("VERDICT: HarperIgnoreLint refused the same payload")
	}

	// Whatever it wrote, wherever it wrote it: a tree is unambiguous in a way a guessed filename is not.
	if err := filepath.WalkDir(config, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			t.Logf("harper wrote %s: %s", strings.TrimPrefix(p, config),
				truncate(strings.TrimSpace(string(b)), 200))
		}
		return nil
	}); err != nil {
		t.Logf("walking %s failed: %v", config, err)
	}
}

// probeLints runs one check cycle for a throwaway document and closes it again, the way production
// does one document at a time.
func probeLints(t *testing.T, h *Harper, text string) []Lint {
	t.Helper()
	uri := h.newURI()
	diags, err := h.checkDiags(text, uri)
	if err != nil {
		t.Fatalf("checkDiags: %v", err)
	}
	lints := h.diagsToLints(diags, text)
	h.closeDoc(uri)
	return lints
}

// pickSpelling is the finding that covers the word, tightest first: a sentence-level rule can cover it
// loosely, and asking about that range would ask harper about the wrong thing.
func pickSpelling(lints []Lint, text, word string) *Lint {
	at := strings.Index(text, word)
	if at < 0 {
		return nil
	}
	var best *Lint
	for i := range lints {
		l := &lints[i]
		if l.CharStart > at || l.CharEnd < at+len(word) {
			continue
		}
		if best == nil || (l.CharEnd-l.CharStart) < (best.CharEnd-best.CharStart) {
			best = l
		}
	}
	return best
}

// execCommand runs one of harper's commands and reports whether the server accepted it. A refusal is
// printed with the server's own words, because that is what separates "wrong payload" from
// "unsupported command" — the distinction the first version of this probe got wrong.
func execCommand(t *testing.T, h *Harper, command string, args []any) bool {
	t.Helper()
	resp, err := h.c.Request("workspace/executeCommand", map[string]any{"command": command, "arguments": args})
	if err != nil {
		t.Logf("executeCommand %s FAILED: %v", command, err)
		return false
	}
	t.Logf("executeCommand %s ok, result=%s", command, truncate(string(resp.Result), 200))
	return true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// commandOf accepts both shapes LSP allows: a Command object, or a bare command name.
func commandOf(action map[string]any) (string, []any) {
	switch c := action["command"].(type) {
	case string:
		return c, nil
	case map[string]any:
		name, _ := c["command"].(string)
		args, _ := c["arguments"].([]any)
		return name, args
	}
	return "", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
