package api

import (
	"net/http"

	"grammar-server/internal/lt"
)

// handleStats reports delivery metrics — counts, sentence shape, the three
// readability scores — for a text. No engine is involved: it is arithmetic over
// the string, so it answers in microseconds on a full-length document and keeps
// working when harper is unavailable.
//
// The scores are English-only (syllable counting drives all three), so an
// unknown language is refused exactly as /v2/check refuses it.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// ponytail: the size guard is spelled out here rather than shared with
	// /v2/check and /v2/fix-sentence. All three collapse into one readText(w, r)
	// helper when the handlers move behind internal/lt.
	if r.ContentLength > maxTextChars*4+4096 {
		writeLTError(w, http.StatusRequestEntityTooLarge,
			"Your text exceeds the limit of %d characters. Please submit a shorter text.", maxTextChars)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTextChars*4+4096)
	req, err := parseCheckRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}
	if n := len([]rune(req.Text)); n > maxTextChars {
		writeLTError(w, http.StatusRequestEntityTooLarge,
			"Your text exceeds the limit of %d characters (it's %d characters). Please submit a shorter text.",
			maxTextChars, n)
		return
	}
	if _, ok := checkLang(w, req.Language); !ok {
		return
	}

	writeJSON(w, 200, lt.Measure(req.Text))
}
