package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The dictionary: the words harper-ls itself should accept.
//
// This is the real thing, not the fallback the ignore list is. harper's own user dictionary is a plain
// line-separated file it reads when the process starts, so a word is added by appending to that file and
// then restarting harper-ls — measured, in both directions:
//
//	a zorbulating word                -> /v2/check reports a misspelling
//	append "zorbulating" to the file, restart harper-ls
//	                                  -> /v2/check reports nothing
//	remove the line, restart          -> the misspelling is back
//
// harper also offers an `Add "<word>" to the user dictionary.` code action. It is not used: it returns
// result=null and changes nothing, before or after a restart, and writes no file (re-measured 2026-10-01;
// the whole investigation is in references/harper-dictionaries.md). The `userDictPath` setting is likewise
// ignored over LSP — which is issue #1707's own title, "does not change the user dictionary location" —
// and that is exactly why this writes to harper's *default* location instead of setting a path.
//
// A word here is one the engine *knows*: every client benefits, LTeX and LibreOffice and the browser
// extension included. A word in the ignore list is only one this server stops reporting, and the UI says
// which is which, because the two are different promises and only one of them is true everywhere.
func dictionaryPath() string {
	// harper-ls's default location, spelled the way harper resolves it: the platform's config root, then
	// harper-ls/dictionary.txt. os.UserConfigDir is precisely that rule — $XDG_CONFIG_HOME or ~/.config on
	// unix, %AppData% on Windows, Application Support on darwin — which is why it is used here rather than
	// the hand-rolled XDG lookup ignorePath does for a file that belongs to this server alone.
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "harper-ls", "dictionary.txt")
}

// listDictionary is the file as written: one word per line, non-empty, trimmed. Blank lines are dropped
// because this is the only place that decides what an entry is, so the read endpoint and the append
// cannot disagree about it. No comment syntax: harper reads every line as a word, so inventing one here
// would put '#' lines into a spell checker's vocabulary.
func listDictionary(path string) []string {
	words := []string{}
	if path == "" {
		return words
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return words
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			words = append(words, line)
		}
	}
	return words
}

// appendDictionary adds one word and reports whether it was new. A word already there is not written
// twice, so the same click twice is a no-op rather than two identical lines. Every other line is kept,
// including one a person added by hand.
func appendDictionary(path, word string) (bool, error) {
	if path == "" {
		return false, os.ErrNotExist
	}
	// Trimmed here rather than trusting the caller: this function is what decides an entry, the same
	// reasoning listDictionary documents for the read side.
	word = strings.TrimSpace(word)
	if word == "" {
		return false, os.ErrInvalid
	}
	lines := listDictionary(path)
	for _, have := range lines {
		if strings.EqualFold(have, word) {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(word + "\n"); err != nil {
		return false, err
	}
	return true, nil
}

// removeDictionary takes one word out and reports whether it was there. Every other line is kept — a word
// typed into the file by hand is not this endpoint's to lose — and the rewrite is temp-and-rename, so a
// failed write cannot leave a half-written dictionary behind.
func removeDictionary(path, word string) (bool, error) {
	if path == "" {
		return false, os.ErrNotExist
	}
	word = strings.TrimSpace(word)
	if word == "" {
		return false, os.ErrInvalid
	}
	kept := []string{}
	found := false
	for _, have := range listDictionary(path) {
		if strings.EqualFold(have, word) {
			found = true
			continue
		}
		kept = append(kept, have)
	}
	if !found {
		return false, nil
	}
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// dictionaryAccepts reports whether harper-ls now accepts the word, by asking it.
//
// The endpoint verifies its own effect instead of reporting what it intended: restarting a process and
// returning 200 is not evidence that the word was picked up, and this product has already shipped an
// endpoint that answered "saved, engine reloaded" while changing nothing. The test is positional — does
// any finding cover exactly this word — so it does not care which rule produced a finding, the same
// rule-free reasoning the ignore filter uses.
func (s *Server) dictionaryAccepts(word string) bool {
	// A carrier sentence, because a bare word is not linted at all: harper treats a fragment as a fragment,
	// so "zorbulating" on its own reports nothing whether or not it is a word, and a verdict built on that
	// would answer true for every input, including a misspelling.
	probe := "a " + word + " here"
	lints, err := s.eng.Check(probe)
	if err != nil {
		return false
	}
	// Offsets are UTF-16 code units, so they go through the same conversion the ignore filter uses. The
	// bounds are the PROBE's: read against the length of the word instead — which is what this did — a
	// finding that starts past the first len(word) bytes is thrown away, and every word comes back
	// "accepted: true", including one harper had just been told to forget. Measured, on exactly that word.
	for _, lint := range lints {
		start, end := u16ToByte(probe, lint.CharStart), u16ToByte(probe, lint.CharEnd)
		if start < 0 || end > len(probe) || start >= end {
			continue
		}
		if strings.EqualFold(probe[start:end], word) {
			return false
		}
	}
	return true
}

// handleDictionary is /v2/dictionary: GET reads the words harper-ls accepts on this machine, POST adds one
// and makes it take effect, DELETE takes one back out and makes that take effect too.
//
// The read is served to anyone who can reach the server, matching /status and /v2/ignore: what the engine
// knows is not a secret, and a client elsewhere can then explain why a word is not flagged. Only changing
// it is machine-local, because the dictionary belongs to the harper-ls this server runs — the same reason
// the ignore list refuses writes from elsewhere.
func (s *Server) handleDictionary(w http.ResponseWriter, r *http.Request) {
	path := dictionaryPath()
	if r.Method == http.MethodGet {
		words := listDictionary(path)
		sort.Strings(words)
		writeJSON(w, 200, map[string]any{
			"words": words, "count": len(words), "path": path,
		})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeError(w, 405, "GET, POST or DELETE /v2/dictionary")
		return
	}
	if !isLoopback(r) {
		writeError(w, 403, "the dictionary can only be changed on the machine the server runs on")
		return
	}
	// POST carries the word in the body; DELETE carries it in the query, so a client that will not put a
	// body on a DELETE can still take a word back out — and `curl -X DELETE '.../v2/dictionary?word=x'` is
	// a complete sentence.
	word := ""
	switch r.Method {
	case http.MethodPost:
		var in struct {
			Word string `json:"word"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			writeError(w, 400, `the body must be JSON: {"word": "..."}`)
			return
		}
		word = in.Word
	case http.MethodDelete:
		word = r.URL.Query().Get("word")
	}
	word = strings.TrimSpace(word)
	if word == "" {
		writeError(w, 400, "word is required")
		return
	}
	if strings.ContainsAny(word, " 	") {
		writeError(w, 400, "the dictionary takes one word, not a phrase")
		return
	}
	action := "added"
	if r.Method == http.MethodDelete {
		action = "removed"
	}
	var changed bool
	var err error
	if action == "removed" {
		changed, err = removeDictionary(path, word)
	} else {
		changed, err = appendDictionary(path, word)
	}
	if err != nil {
		writeError(w, 500, "could not write the dictionary: %v", err)
		return
	}
	if changed {
		// Every answer the engine has already given for this text is now suspect, and a stale one is
		// exactly what makes a taught word look like it did not work — measured: the same sentence
		// reported the misspelling after the word was written, until this line existed. It cuts the same
		// way for a removal, where the stale answer is the one that says the word is fine.
		s.cache.clear()
	}
	// harper-ls reads the file at startup, so the word only counts once the process has restarted.
	// Reconnect() is Stop + Start + initialize + re-push the config — a genuinely fresh process, whatever
	// its doc comment says — and the engine already reconnects from Check's error path, so this introduces
	// no new concurrency.
	//
	// ponytail: the restart is global. A check in flight while a word is added fails and is retried once by
	// the engine; that is the ceiling. Per-word reload without a restart is the upgrade path, and it is not
	// available in harper-ls 2.11.0 — the commands that would do it are no-ops.
	reloaded := true
	if err := s.eng.Reconnect(); err != nil {
		reloaded = false
	}
	accepted := reloaded && s.dictionaryAccepts(word)
	// The path and the verdict both come back: a client can name the file, and it can tell a word that
	// took effect from one that is merely written down. `accepted` after a removal is a real question —
	// harper knows plenty of words on its own — so it is measured rather than assumed false.
	report := map[string]any{
		"word": word, "changed": changed, "reloaded": reloaded, "accepted": accepted,
		"count": len(listDictionary(path)), "path": path,
	}
	report[action] = changed // "added" or "removed": the answer says which verb it performed
	writeJSON(w, 200, report)
}
