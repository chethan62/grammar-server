package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The ignore list: the words this server stops reporting.
//
// Deliberately not a dictionary, and not called one. Harper's own dictionaries are where a user's
// vocabulary belongs, and they do not work over LSP in this version — four mechanisms measured, all
// documented in references/harper-dictionaries.md — so this is the honest fallback rather than a
// pretend version of the real thing: **the engine still flags the word, and every other editor still
// shows it.** What changes is only that this product stops repeating itself, and the UI says so.
//
// A plain file beside the other settings — one word per line, '#' comments — because a person has to
// be able to read it, edit it, and delete a line by hand when a card is clicked by mistake.
func ignorePath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grammar-server", "ignored-words")
}

// listIgnored is the list as written: the word a person typed, not the lower-cased form matching uses.
// Comments and blank lines are dropped — they belong to whoever wrote the file — and this is the only
// place that decides what counts as an entry, so the matching rule and the read endpoint cannot
// disagree about it.
func listIgnored(path string) []string {
	words := []string{}
	if path == "" {
		return words
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return words
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words = append(words, line)
	}
	return words
}

// readIgnored is that list, lower-cased for matching: "Zorbulating" and "zorbulating" are the same
// word to a person, and a list that matched only one of them would read as broken.
func readIgnored(path string) map[string]bool {
	words := map[string]bool{}
	for _, word := range listIgnored(path) {
		words[strings.ToLower(word)] = true
	}
	return words
}

// ignoredText reports whether a finding's own text is an ignored word.
//
// An exact match on the trimmed finding, deliberately: a substring rule would let one ignored word
// swallow every finding that happens to contain it, and a sentence-level finding is not a word at all
// so it is never swallowed. This is the whole filter — it does not care which rule produced the
// finding, which is why ignoring works for a phrase as well as for a misspelling.
func ignoredText(text string, start, end int, words map[string]bool) bool {
	if len(words) == 0 {
		return false
	}
	s, e := u16ToByte(text, start), u16ToByte(text, end)
	if s >= e || e > len(text) {
		return false
	}
	return words[strings.ToLower(strings.TrimSpace(text[s:e]))]
}

// writeIgnored adds or removes one word. Every other line is kept — a comment a person wrote is not
// ours to lose — and a word already in the list is not written twice.
func writeIgnored(path, word string, forget bool) error {
	if path == "" {
		return os.ErrNotExist
	}
	existing, _ := os.ReadFile(path) // a missing file is an empty list, not a failure
	lines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		lines = nil
	}
	kept := make([]string, 0, len(lines)+1)
	found := false
	for _, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), word) {
			found = true
			if forget {
				continue // dropping the line is the whole of "forget"
			}
		}
		kept = append(kept, line)
	}
	if !forget && !found {
		kept = append(kept, word)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(kept) == 0 {
		// Nothing left is better as no file: "forget" that leaves a file behind looks like something
		// is still ignored, and nothing reads a file to find nothing.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// handleIgnore is /v2/ignore: GET reads the list, POST adds a word to it or takes one back with
// "forget": true.
//
// The read is served to anyone who can reach the server, the same posture /status takes: the list is
// the engine's, and what it stops reporting is not a secret. Only *changing* it is a machine-local act,
// because a word added over the LAN would quietly change what everybody else sees.
func (s *Server) handleIgnore(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		words := listIgnored(ignorePath())
		sort.Strings(words)
		writeJSON(w, 200, map[string]any{
			"words": words, "count": len(words), "path": ignorePath(),
		})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "GET or POST /v2/ignore")
		return
	}
	if !isLoopback(r) {
		writeError(w, 403, "the ignore list can only be changed on the machine the server runs on")
		return
	}
	var in struct {
		Word   string `json:"word"`
		Forget bool   `json:"forget"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeError(w, 400, `the body must be JSON: {"word": "..."}`)
		return
	}
	word := strings.TrimSpace(in.Word)
	if word == "" {
		writeError(w, 400, "word is required")
		return
	}
	path := ignorePath()
	if err := writeIgnored(path, word, in.Forget); err != nil {
		writeError(w, 500, "could not write the ignore list: %v", err)
		return
	}
	// The path comes back so a client can name the file in its confirmation, wherever it is running:
	// "remove the line from ..." is only actionable if the client is told where the line is.
	writeJSON(w, 200, map[string]any{
		"word": word, "forgot": in.Forget, "ignored": len(readIgnored(path)), "path": path,
	})
}
