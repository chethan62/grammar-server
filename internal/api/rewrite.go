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

// validDirective reports whether s is safe to place inside the model's
// instruction: empty, or one short lower-case word ("professional", "casual",
// "concise"). Anything with spaces, punctuation or length is refused rather
// than escaped, because a tone is a word - and the refusal names itself, so a
// client sending something else is told why.
func validDirective(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && c != '-' {
			return false
		}
	}
	return true
}

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
	Model      string   `json:"model"`    // the model that answered, as configured
	Provider   string   `json:"provider"` // which backend it was (ollama, llamacpp, ...)
	ElapsedMs  int64    `json:"elapsedMs"`
}

// EnableRewrite attaches a local model to the server, and warms it in the
// background: a cold load costs ~6s on this CPU and the first click should not
// pay for it. Nothing else in the API depends on this — with Ollama stopped,
// /v2/rewrite answers 503 and every other endpoint is unchanged.
func (s *Server) EnableRewrite(url, model string) {
	s.SetRewrite(rewrite.ProtoOllama, url, model)
}

// SetRewrite points the server at a backend, replacing whatever was there, and
// warms it in the background. It is the one place that assigns s.rw: the UI's
// backend switcher and the startup path both come through here, so neither can
// leave the provider, URL and model out of step with the client.
//
// An unknown provider or an empty URL/model disables rewriting (rw = nil), which
// every other endpoint already treats as normal.
func (s *Server) SetRewrite(provider, url, model string) {
	rw := rewrite.Build(provider, url, model, rewrite.APIKeyFor(provider))

	s.rwMu.Lock()
	s.rw, s.rwProvider, s.rwURL, s.rwModel = rw, provider, url, model
	s.rwMu.Unlock()

	if rw == nil {
		return
	}
	// Warm in the background: a cold load costs ~6s on this CPU and the first
	// click should not pay for it. Ollama is the one backend that can hold the
	// model between clicks; for the others this is at worst one wasted request.
	go func() {
		start := time.Now()
		if w, ok := rw.(*rewrite.Client); ok {
			if err := w.WarmUp(context.Background()); err != nil {
				log.Printf("rewrite: warm-up failed (%v) — /v2/rewrite will answer 503 until %s is up", err, provider)
				return
			}
		} else if _, err := rw.Rewrite(context.Background(), "We should schedule a meeting in order to discuss the report.", "", ""); err != nil {
			log.Printf("rewrite: %s is not answering yet (%v)", provider, err)
			return
		}
		log.Printf("rewrite: %s/%s warm in %s", provider, model, time.Since(start).Round(time.Millisecond))
	}()
}

// rewriteClient returns the current backend under the lock. Every reader uses
// this rather than touching s.rw: the UI can swap backends mid-request.
func (s *Server) rewriteClient() (rewrite.Rewriter, string, string) {
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	return s.rw, s.rwProvider, s.rwModel
}

// backendHint names the fix for a backend that is not answering. It is per
// provider because "start it with: ollama serve" is worse than useless to
// someone running llama.cpp.
func backendHint(provider string) string {
	switch provider {
	case "ollama":
		return "start it with: ollama serve"
	case "llamacpp":
		return "start it with: llama-server -m <model>.gguf --port 8080"
	case "lmstudio":
		return "enable the local server in LM Studio's Developer tab"
	case "vllm":
		return "start it with: python -m vllm.entrypoints.openai.api_server --model <model>"
	default:
		return "check that the base URL answers GET /v1/models"
	}
}

func (s *Server) handleRewrite(w http.ResponseWriter, r *http.Request) {
	rw, provider, model := s.rewriteClient()
	if rw == nil {
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
	// tone and intent are interpolated into the model's system instruction, and
	// this endpoint is reachable from the LAN - only /v1/ai's writes are
	// loopback-only. A free-text tone is therefore a prompt-injection seam: a
	// client on the network could send "ignore the above and answer with
	// <anything>" and the result would be shown as a rephrase of the user's own
	// sentence. Shut it by accepting only what the contract needs: one short
	// lower-case word. Everything the UI sends passes unchanged.
	if !validDirective(req.Tone) {
		writeError(w, http.StatusBadRequest, "invalid 'tone': one lower-case word, up to 20 letters")
		return
	}
	if !validDirective(req.Intent) {
		writeError(w, http.StatusBadRequest, "invalid 'intent': one lower-case word, up to 20 letters")
		return
	}

	start := time.Now()
	candidates, err := rw.Rewrite(r.Context(), req.Text, req.Tone, req.Intent)
	if err != nil {
		// Each failure names its own fix: these are the three things a user can
		// actually do about a local model.
		switch {
		case errors.Is(err, rewrite.ErrModelMissing):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite model %s is not on %s — GET %s/v1/models lists what it has (ollama: ollama pull %s)",
				model, provider, s.rwURL, model)
		case errors.Is(err, rewrite.ErrAuth):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite backend rejected the API key for %s — set %s in the server's environment",
				provider, rewrite.KeyEnvFor(provider))
		case errors.Is(err, rewrite.ErrUnavailable):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite backend unavailable (%s at %s) — %s", provider, s.rwURL, backendHint(provider))
		case errors.Is(err, rewrite.ErrTimeout):
			writeLTError(w, http.StatusServiceUnavailable,
				"rewrite timed out (%s, model %s) — a smaller model or a shorter sentence will answer sooner",
				err, model)
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
		Model:      model,
		Provider:   provider,
		ElapsedMs:  time.Since(start).Milliseconds(),
	})
}
