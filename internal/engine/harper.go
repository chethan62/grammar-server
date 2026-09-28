// Package engine drives harper-ls via LSP and yields normalized lints with UTF-16
// offsets and replacement suggestions (the shape LanguageTool's API expects).
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"grammar-server/internal/lsp"
)

// Lint is a single grammar/spelling finding.
type Lint struct {
	Rule         string   `json:"rule"`
	Kind         string   `json:"kind"`
	Message      string   `json:"message"`
	CharStart    int      `json:"charStart"` // UTF-16 code unit offset in full text
	CharEnd      int      `json:"charEnd"`
	Replacements []string `json:"replacements"`

	// LSP position of the finding (line/character in UTF-16 code units).
	// Kept so codeAction requests address the real location: a flat offset
	// replayed as {line: 0, character: N} is only valid for single-line text.
	StartLine, StartChar int
	EndLine, EndChar     int
}

// Harper wraps a single persistent harper-ls --stdio process.
type Harper struct {
	// mu serializes whole check cycles (didOpen → publishDiagnostics →
	// codeAction). Concurrent checks would otherwise race on the shared
	// Notifications channel: each waiter only accepts diagnostics for its own
	// URI and drops every other publish, so the owner of a dropped publish
	// blocks until timeout and returns zero matches.
	mu      sync.Mutex
	c       *lsp.Client
	version int
	config  json.RawMessage // {"harper-ls":{...}} full wrapper
	bin     string
	dialect string   // current dialect (for reconnects)
	dis     []string // disabled rule names (kept for dialect switches)
}

var kindMap = map[string]string{
	"SpellCheck":                "grammar",
	"The":                       "grammar",
	"CapitalizePersonalPronouns": "style",
	"SentenceCapitalization":     "style",
	"LeftRightHand":              "grammar",
	"SpellCheckCompound":         "grammar",
	"LackOfConjunction":          "style",
	"Spaces":                     "typography",
}

// NewHarper spawns harper-ls and completes the LSP handshake.
func NewHarper(bin, dialect string, disabled []string) (*Harper, error) {
	c, err := lsp.Start(bin, "--stdio")
	if err != nil {
		return nil, err
	}
	h := &Harper{c: c, bin: bin, dialect: dialect, dis: disabled}

	_, err = c.Request("initialize", map[string]any{
		"processId":    nil,
		"capabilities": map[string]any{},
		"rootUri":      "file:///tmp",
	})
	if err != nil {
		c.Stop()
		return nil, err
	}
	if err := c.Notify("initialized", map[string]any{}); err != nil {
		c.Stop()
		return nil, err
	}
	if err := h.setConfig(dialect, disabled); err != nil {
		c.Stop()
		return nil, err
	}
	// Warm up: run one lint so harper-ls loads word lists/caches before any
	// real request (otherwise the first spell-check batch comes back empty).
	_ = h.checkDiags("This is the quick brown fox jumping over the lazy dog.", h.newURI())
	return h, nil
}

// setConfig builds the full linter map (unlisted rules = disabled for harper-ls)
// and pushes it via didChangeConfiguration.
func (h *Harper) setConfig(dialect string, disabled []string) error {
	dis := map[string]bool{}
	for _, r := range disabled {
		dis[r] = true
	}
	linters := map[string]bool{}
	if out, err := exec.Command(h.cliBin(), "config").Output(); err == nil {
		var rules map[string]struct {
			DefaultValue bool `json:"default_value"`
		}
		if json.Unmarshal(out, &rules) == nil {
			for name, info := range rules {
				// enabled unless user-disabled or the rule is off by default
				linters[name] = !dis[name] && info.DefaultValue
			}
		}
	}
	if len(linters) == 0 {
		for _, name := range []string{"SpellCheck", "SpellCheckCompound", "The",
			"CapitalizePersonalPronouns", "SentenceCapitalization", "LeftRightHand",
			"Spaces", "LackOfConjunction"} {
			linters[name] = !dis[name]
		}
	}
	h.config, _ = json.Marshal(map[string]any{
		"harper-ls": map[string]any{"linters": linters, "dialect": dialect},
	})
	return h.c.Notify("workspace/didChangeConfiguration", map[string]any{
		"settings": json.RawMessage(h.config),
	})
}

func (h *Harper) cliBin() string {
	if cli := os.Getenv("HARPER_CLI"); cli != "" {
		return cli
	}
	// Fallback: "harper-ls" -> "harper-cli"
	return h.bin[:len(h.bin)-3] + "cli"
}

// SetDialect reconfigures the engine for a different English dialect.
func (h *Harper) SetDialect(dialect string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if dialect == h.dialect {
		return nil // already configured; skip a redundant didChangeConfiguration
	}
	h.dialect = dialect
	if err := h.ensureAlive(); err != nil {
		return err
	}
	return h.setConfig(dialect, h.dis)
}

// Close terminates the harper-ls subprocess.
func (h *Harper) Close() { h.c.Stop() }

// Reconnect restarts the harper-ls subprocess if it has crashed.
func (h *Harper) Reconnect() error {
	h.c.Stop()
	c, err := lsp.Start(h.bin, "--stdio")
	if err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}
	h.c = c
	_, err = c.Request("initialize", map[string]any{"processId": nil, "capabilities": map[string]any{}, "rootUri": "file:///tmp"})
	if err != nil {
		return err
	}
	if err := c.Notify("initialized", map[string]any{}); err != nil {
		return err
	}
	// Re-push the full config so subsequent checks work
	if err := h.setConfig(h.dialect, h.dis); err != nil {
		return err
	}
	return nil
}

// Check lints text and returns matches. Each check uses a unique document URI
// so diagnostics from concurrent checks can never be cross-matched.
// If the harper-ls subprocess has crashed, it will be reconnected automatically
// and the check retried once.
func (h *Harper) Check(text string) ([]Lint, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensureAlive(); err != nil {
		return nil, err
	}
	uri := h.newURI()
	diags := h.checkDiags(text, uri)
	l := h.diagsToLints(diags, text)
	h.enrich(uri, diags, l)
	return l, nil
}

// ensureAlive checks whether the LSP connection is healthy, reconnecting if not.
func (h *Harper) ensureAlive() error {
	// Quick probe: send a no-op notification (didOpen with empty text).
	// If the pipe is broken, Reconnect().
	err := h.c.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        "file:///tmp/_probe.md",
			"languageId": "markdown",
			"version":    1,
			"text":       "",
		},
	})
	if err != nil {
		return h.Reconnect()
	}
	return nil
}

// newURI returns a fresh, unique document URI per check.
func (h *Harper) newURI() string {
	h.version++
	return fmt.Sprintf("file:///tmp/harper-live-%d.md", h.version)
}

// checkDiags sends didOpen and reads until publishDiagnostics for uri.
func (h *Harper) checkDiags(text, uri string) []json.RawMessage {
	if err := h.c.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "markdown",
			"version":    1,
			"text":       text,
		},
	}); err != nil {
		return nil
	}

	var diags []json.RawMessage
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
loop:
	for {
		select {
		case m := <-h.c.ServerRequests:
			h.handleServerRequest(m)
		case m := <-h.c.Notifications:
			if m.Method == "textDocument/publishDiagnostics" {
				var p struct {
					URI          string            `json:"uri"`
					Diagnostics []json.RawMessage `json:"diagnostics"`
				}
				if json.Unmarshal(m.Params, &p) == nil && p.URI == uri {
					diags = p.Diagnostics
					break loop
				}
			}
		case <-h.c.Err():
			return nil
		case <-timeout.C:
			break loop
		}
	}
	return diags
}

// enrich fetches codeAction suggestions for each diagnostic (harper-ls returns
// actions for one diagnostic per query, so we query per-lint).
func (h *Harper) enrich(uri string, diags []json.RawMessage, lints []Lint) {
	for i := range lints {
		d := &lints[i]
		req := map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"range":        d.rangeMap(),
			"context": map[string]any{
				"diagnostics": []any{d.diagMap()},
			},
		}
		resp, err := h.c.Request("textDocument/codeAction", req)
		if err != nil {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(resp.Result, &items) != nil {
			continue
		}
		for _, it := range items {
			var act struct {
				Title string `json:"title"`
				Edit  *struct {
					Changes map[string][]struct {
						NewText string `json:"newText"`
					} `json:"changes"`
				} `json:"edit"`
			}
			if json.Unmarshal(it, &act) != nil || act.Edit == nil {
				continue
			}
			for _, chglist := range act.Edit.Changes {
				for _, ch := range chglist {
					d.Replacements = append(d.Replacements, ch.NewText)
				}
			}
		}
	}
}

func (h *Harper) handleServerRequest(m lsp.Message) {
	if m.Method == "workspace/configuration" {
		// respond with the full wrapper object in a 1-element array
		result := "[" + string(h.config) + "]"
		_ = h.c.Send(lsp.Message{JSONRPC: "2.0", ID: m.ID, Result: json.RawMessage(result)})
		return
	}
	_ = h.c.Send(lsp.Message{JSONRPC: "2.0", ID: m.ID, Result: json.RawMessage(`null`)})
}

// diagsToLints converts LSP diagnostics into Lints with UTF-16 offsets.
func (h *Harper) diagsToLints(diags []json.RawMessage, text string) []Lint {
	var out []Lint
	for _, d := range diags {
		var diag struct {
			Range struct {
				Start struct{ Line, Character int } `json:"start"`
				End   struct{ Line, Character int } `json:"end"`
			} `json:"range"`
			Severity int    `json:"severity"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		}
		if json.Unmarshal(d, &diag) != nil {
			continue
		}
		start := lineCharToU16Offset(text, diag.Range.Start.Line, diag.Range.Start.Character)
		end := lineCharToU16Offset(text, diag.Range.End.Line, diag.Range.End.Character)
		if start < 0 {
			continue
		}
		code := diag.Code
		if code == "" {
			code = "Unknown"
		}
		kind := kindMap[code]
		if kind == "" {
			kind = "grammar"
		}
		out = append(out, Lint{
			Rule:      code,
			Kind:      kind,
			Message:   diag.Message,
			CharStart: start,
			CharEnd:   end,
			StartLine: diag.Range.Start.Line,
			StartChar: diag.Range.Start.Character,
			EndLine:   diag.Range.End.Line,
			EndChar:   diag.Range.End.Character,
		})
	}
	return out
}

// lineCharToU16Offset converts an LSP (line, character) position — characters
// measured in UTF-16 code units — into a UTF-16 code unit offset in text.
func lineCharToU16Offset(text string, line, char int) int {
	lines := strings.Split(text, "\n")
	if line >= len(lines) {
		return -1
	}
	if char > u16Len(lines[line]) {
		return -1
	}
	off := 0
	for i := 0; i < line; i++ {
		off += u16Len(lines[i]) + 1 // +1 for the \n separator
	}
	return off + char
}

// u16Len returns the UTF-16 code unit length of a string.
func u16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf16Len returns the UTF-16 code unit count of a single rune.
func utf16Len(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// rangeMap builds an LSP range for the lint from its real line/character
// position. Deriving this from the flat UTF-16 offset as {line: 0, ...} breaks
// on any multi-line document: harper-ls looks up a position that does not
// exist and returns no code actions, so replacements come back empty.
func (l *Lint) rangeMap() map[string]any {
	return map[string]any{
		"start": map[string]any{"line": l.StartLine, "character": l.StartChar},
		"end":   map[string]any{"line": l.EndLine, "character": l.EndChar},
	}
}

// diagMap builds a minimal LSP diagnostic for a codeAction request.
func (l *Lint) diagMap() map[string]any {
	return map[string]any{
		"code":     l.Rule,
		"message":  l.Message,
		"severity": 2,
		"source":   "Harper",
		"range":    l.rangeMap(),
	}
}
