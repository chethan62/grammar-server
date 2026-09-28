// Package engine drives harper-ls via LSP and yields normalized lints with UTF-16
// offsets and replacement suggestions (the shape LanguageTool's API expects).
package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	en      []string // rule names switched on by a client (kept for reconnects)
	dis     []string // disabled rule names (kept for dialect switches)
	only    bool     // enabledOnly: run just the rules in en

	ruleOnce sync.Once // the rule list is read from the CLI once
	rules    map[string]ruleInfo
	ruleErr  error
}

// diagnosticsTimeout bounds one document's lint. harper answers in milliseconds;
// ten seconds is the point past which waiting has stopped being useful.
const diagnosticsTimeout = 10 * time.Second

var kindMap = map[string]string{
	"SpellCheck":                 "grammar",
	"The":                        "grammar",
	"CapitalizePersonalPronouns": "style",
	"SentenceCapitalization":     "style",
	"LeftRightHand":              "grammar",
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
	if err := h.setConfig(dialect, nil, disabled, false); err != nil {
		c.Stop()
		return nil, err
	}
	// Warm up: run one lint so harper-ls loads word lists/caches before any
	// real request (otherwise the first spell-check batch comes back empty).
	_, _ = h.checkDiags("This is the quick brown fox jumping over the lazy dog.", h.newURI())
	return h, nil
}

// setConfig builds the full linter map (unlisted rules = disabled for harper-ls)
// and pushes it via didChangeConfiguration.
//
// Harper's config means "everything not listed is off", so the map is derived
// from harper-cli's own rule list rather than hand-written: a rule stays on when
// it is on by default (or a client asked for it by name) and is not disabled.
// only — LanguageTool's enabledOnly — drops the defaults and leaves just the
// requested set.
func (h *Harper) setConfig(dialect string, enabled, disabled []string, only bool) error {
	en := map[string]bool{}
	for _, r := range enabled {
		en[r] = true
	}
	dis := map[string]bool{}
	for _, r := range disabled {
		dis[r] = true
	}
	linters := map[string]bool{}
	rules, err := h.ruleList()
	if err != nil {
		// Any rule missing from this map is off, so the fallback silently drops the
		// engine from hundreds of rules to eight. Say it out loud: the service still
		// answers, and nothing in the response says it is running crippled.
		log.Printf("engine: reading harper's rule list from %s failed: %v; falling back to %d built-in rules",
			h.cliBin(), err, len(fallbackRules))
		for _, name := range fallbackRules {
			linters[name] = !dis[name]
		}
	} else {
		for name, info := range rules {
			linters[name] = !dis[name] && (en[name] || (!only && info.DefaultValue))
		}
	}
	h.config, _ = json.Marshal(map[string]any{
		"harper-ls": map[string]any{"linters": linters, "dialect": dialect},
	})
	return h.c.Notify("workspace/didChangeConfiguration", map[string]any{
		"settings": json.RawMessage(h.config),
	})
}

// fallbackRules is the hand-written subset used when the paired CLI cannot be
// read. Every other harper rule is simply off in that state.
var fallbackRules = []string{"SpellCheck", "The", "CapitalizePersonalPronouns",
	"SentenceCapitalization", "LeftRightHand", "Spaces"}

// cliBin is the harper-cli that ships beside harper-ls: it is how the rule list is
// read, so a wrong path here means a crippled engine rather than an error.
func (h *Harper) cliBin() string {
	if cli := os.Getenv("HARPER_CLI"); cli != "" {
		return cli
	}
	// "harper-ls" sits next to "harper-cli": swap the suffix, never chop it.
	// Chopping produced "harpercli", which resolves nowhere.
	if strings.HasSuffix(h.bin, "-ls") {
		return strings.TrimSuffix(h.bin, "-ls") + "-cli"
	}
	return "harper-cli"
}

// ruleInfo is one entry of harper's rule list: its default on/off state.
type ruleInfo struct {
	DefaultValue bool `json:"default_value"`
}

// ruleList reads harper's rule defaults, once per engine.
//
// The list comes from the paired CLI, a 150 MB process that takes ~0.7 s to print
// it, and it was re-read on every configuration change — which made a rule toggle
// or a dialect switch cost that fork rather than the change itself.
func (h *Harper) ruleList() (map[string]ruleInfo, error) {
	h.ruleOnce.Do(func() {
		out, err := exec.Command(h.cliBin(), "config").Output()
		if err != nil {
			h.ruleErr = err
			return
		}
		var rules map[string]ruleInfo
		if err := json.Unmarshal(out, &rules); err != nil {
			h.ruleErr = fmt.Errorf("unparseable rule list: %w", err)
			return
		}
		if len(rules) == 0 {
			h.ruleErr = errors.New("empty rule list")
			return
		}
		h.rules = rules
	})
	return h.rules, h.ruleErr
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
	return h.setConfig(dialect, h.en, h.dis, h.only)
}

// SetRules turns individual rules on and off for the checks that follow.
// enabled names rules to switch on beyond the defaults (the off-by-default
// rules — BoringWords, NoOxfordComma, SpelledNumbers, PossessiveNoun, …),
// disabled names rules to switch off, and only is LanguageTool's enabledOnly:
// run nothing but the enabled set.
//
// Reconfiguring harper-ls costs a didChangeConfiguration round trip, so the
// applied set is remembered and an unchanged request is a no-op. Clients send
// their toggles on every check; paying for them once is the difference between
// a rule filter and a per-request tax.
func (h *Harper) SetRules(enabled, disabled []string, only bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.only == only && sameStrings(h.en, enabled) && sameStrings(h.dis, disabled) {
		return nil
	}
	if err := h.ensureAlive(); err != nil {
		return err
	}
	if err := h.setConfig(h.dialect, enabled, disabled, only); err != nil {
		return err
	}
	h.en, h.dis, h.only = append([]string(nil), enabled...), append([]string(nil), disabled...), only
	return nil
}

// sameStrings reports set equality for two rule-name lists, order-insensitive.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
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
	if err := h.setConfig(h.dialect, h.en, h.dis, h.only); err != nil {
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
	diags, err := h.checkDiags(text, uri)
	if err != nil {
		// harper-ls sometimes goes quiet: the process stays up and stops answering.
		// There is nothing to repair in it from here, and a fresh process always
		// answers, so one reconnect-and-retry turns a wedged engine into a slow
		// answer instead of a 500 for every request that follows.
		if rerr := h.Reconnect(); rerr != nil {
			return nil, fmt.Errorf("%w (reconnect failed: %v)", err, rerr)
		}
		uri = h.newURI()
		if diags, err = h.checkDiags(text, uri); err != nil {
			return nil, err
		}
	}
	// Close the document whatever happens: an open document is one harper-ls keeps
	// in memory and re-lints on every configuration change, so leaving them open
	// grew memory without bound (318 MB peak).
	defer h.closeDoc(uri)
	l := h.diagsToLints(diags, text)
	h.enrich(uri, diags, l)
	return l, nil
}

// closeDoc tells harper-ls we are finished with a document. Each check opens a
// fresh URI, so without this harper-ls accumulates one document per check.
func (h *Harper) closeDoc(uri string) {
	_ = h.c.Notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// ensureAlive reconnects if the harper-ls connection has died.
//
// It used to probe by re-opening the same probe document on every call. That is a
// protocol violation — the document is never closed — and it put a didOpen
// immediately before every configuration change, which is exactly the interleaving
// that left harper-ls alive but publishing nothing. Liveness is now read off the
// connection instead: the read loop closes its channel when it stops.
func (h *Harper) ensureAlive() error {
	select {
	case <-h.c.Done():
		return h.Reconnect()
	default:
		return nil
	}
}

// newURI returns a fresh, unique document URI per check.
func (h *Harper) newURI() string {
	h.version++
	return fmt.Sprintf("file:///tmp/harper-live-%d.md", h.version)
}

// checkDiags sends didOpen and reads until publishDiagnostics for uri.
//
// A timeout is an error, not an empty result: harper-ls always publishes a
// diagnostics array (empty when the text is clean), so treating silence as "no
// problems" would report an unchecked document as clean.
func (h *Harper) checkDiags(text, uri string) ([]json.RawMessage, error) {
	if err := h.c.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "markdown",
			"version":    1,
			"text":       text,
		},
	}); err != nil {
		return nil, err
	}

	var diags []json.RawMessage
	timeout := time.NewTimer(diagnosticsTimeout)
	defer timeout.Stop()
loop:
	for {
		select {
		case m := <-h.c.ServerRequests:
			h.handleServerRequest(m)
		case m := <-h.c.Notifications:
			if m.Method == "textDocument/publishDiagnostics" {
				var p struct {
					URI         string            `json:"uri"`
					Diagnostics []json.RawMessage `json:"diagnostics"`
				}
				if json.Unmarshal(m.Params, &p) == nil && p.URI == uri {
					diags = p.Diagnostics
					break loop
				}
			}
		case <-h.c.Err():
			return nil, fmt.Errorf("harper-ls connection closed")
		case <-timeout.C:
			break loop
		}
	}
	if diags == nil {
		return nil, fmt.Errorf("harper-ls did not answer within %s", diagnosticsTimeout)
	}
	return diags, nil
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
