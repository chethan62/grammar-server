package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"grammar-server/internal/rewrite"
)

// maxRewriteChars caps one rewrite request. A rewrite is a sentence or two: the
// model is ~1s per 20 tokens of output, so a whole document would hold a CPU for
// minutes. Callers chunk; 2,000 characters is a long paragraph.
const maxRewriteChars = 2_000

// RewriteRequest is the body of POST /v2/rewrite.
type RewriteRequest struct {
	Text     string `json:"text"`
	Language string `json:"language"`
	Tone     string `json:"tone"`   // optional: "formal", "casual", ...
	Intent   string `json:"intent"` // optional: "concise", "clear", ...
}

// RewriteResponse carries the alternatives. model and elapsedMs are reported so
// a UI can show what answered and how long it took — with local models, that is
// the difference between "slow" and "broken".
type RewriteResponse struct {
	Candidates []string `json:"candidates"`
	Model      string   `json:"model"`
	ElapsedMs  int64    `json:"elapsedMs"`
}

// EnableRewrite attaches a local model to the server, and warms it in the
// background: a cold load costs ~6s on this CPU and the first click should not
// pay for it. Nothing else in the API depends on this — with Ollama stopped,
// /v2/rewrite answers 503 and every other endpoint is unchanged.
func (s *Server) EnableRewrite(url, model string) {
	if url == "" || model == "" {
		return
	}
	s.rw = rewrite.New(url, model)
	go func() {
		start := time.Now()
		if err := s.rw.WarmUp(context.Background()); err != nil {
			log.Printf("rewrite: warm-up failed (%v) — /v2/rewrite will answer 503 until the backend is up", err)
			return
		}
		log.Printf("rewrite: %s warm in %s", s.rw.Model, time.Since(start).Round(time.Millisecond))
	}()
}

func (s *Server) handleRewrite(w http.ResponseWriter, r *http.Request) {
	if s.rw == nil {
		writeLTError(w, http.StatusServiceUnavailable,
			"rewriting is not configured on this server (set rewrite_model in the config to enable it)")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !capBody(w, r, maxRewriteChars, rewriteTooLong) {
		return
	}
	var req RewriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}
	if textTooLong(w, req.Text, maxRewriteChars, rewriteGot) {
		return
	}
	if _, ok := checkLang(w, req.Language); !ok {
		return
	}

	start := time.Now()
	candidates, err := s.rw.Rewrite(r.Context(), req.Text, req.Tone, req.Intent)
	if err != nil {
		// Each failure names its own fix: these are the three things a user can
		// actually do about a local model.
		switch {
		case errors.Is(err, rewrite.ErrModelMissing):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite model %s is not installed — run: ollama pull %s", s.rw.Model, s.rw.Model)
		case errors.Is(err, rewrite.ErrUnavailable):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite backend unavailable — start it with: ollama serve")
		case errors.Is(err, rewrite.ErrTimeout):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite timed out (%s, model %s) — a smaller model or a shorter sentence will answer sooner",
				err, s.rw.Model)
		default:
			writeLTError(w, http.StatusServiceUnavailable, "rewrite failed: %v", err)
		}
		return
	}
	if len(candidates) == 0 {
		writeLTError(w, http.StatusServiceUnavailable, "rewrite returned nothing usable — try again")
		return
	}

	writeJSON(w, 200, RewriteResponse{
		Candidates: candidates,
		Model:      s.rw.Model,
		ElapsedMs:  time.Since(start).Milliseconds(),
	})
}
