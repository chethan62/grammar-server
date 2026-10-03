package api

import (
	"net/http"
	"strconv"
	"strings"
)

// completions returns the words that begin with prefix, this machine's own dictionary first.
//
// The order between the two sources is the point. A word the user taught the engine comes back before
// harper's own list — typing "spec" offers the taught "specular" ahead of harper's "specialist" — because the
// word someone added is the one they are reaching for again. harper's list arrives alphabetical, so a plain
// scan keeps that order without sorting anything.
//
// The prefix is expected lowercased (the handler's job) and matching ignores case, because a word is one word
// however someone capitalises it.
//
// Possessives sort last within each source, and are not dropped: "spec" should offer speculative before
// speck's — a form you would type is not the word you are completing — while "didn" still reaches "didn't".
//
// ponytail: a prefix scan over 134,882 words per request, ~1 ms in this process. Build an index if a client
// ever asks for completions on every keystroke of a long word.
func completions(prefix string, mine, harper []string, limit int) []string {
	out := make([]string, 0, limit)
	seen := make(map[string]bool, limit)
	for _, source := range [][]string{mine, harper} {
		for _, possessive := range []bool{false, true} {
			for _, word := range source {
				if len(out) >= limit {
					return out
				}
				key := strings.ToLower(word)
				if seen[key] || !strings.HasPrefix(key, prefix) || strings.Contains(key, "'") != possessive {
					continue
				}
				seen[key] = true
				out = append(out, word)
			}
		}
	}
	return out
}

// handleComplete is /v2/complete?prefix=…: the words that start with it, for completing a word as it is
// typed. Read-only, and served to anyone who can reach the server for the reason GET /v2/dictionary is —
// what the engine knows is not a secret, and a client on another machine can offer the same completions.
func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	prefix := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prefix")))
	limit := 8
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 25 {
			limit = n
		}
	}
	// One letter is the alphabet, not a completion — and answering before harper is consulted is what keeps
	// this cheap for a client that asks on the first keystroke.
	if len(prefix) < 2 {
		writeJSON(w, 200, map[string]any{"prefix": prefix, "words": []string{}, "count": 0})
		return
	}
	words, err := s.eng.Words()
	if err != nil {
		// 503 rather than an empty list: "harper-cli is not installed beside harper-ls" is a different fact
		// from "no word starts with this", and a client that cannot tell them apart reports the wrong one.
		writeError(w, 503, "harper's word list is unavailable: %v", err)
		return
	}
	found := completions(prefix, listDictionary(dictionaryPath()), words, limit)
	writeJSON(w, 200, map[string]any{"prefix": prefix, "words": found, "count": len(found)})
}
