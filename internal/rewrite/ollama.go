// Package rewrite asks a local Ollama model to rephrase a sentence.
//
// It is the only part of grammar-server that needs a model: it is never on the
// /v2/check path, every endpoint behaves identically with Ollama stopped, and
// the server never downloads a model behind your back.
package rewrite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds one rewrite. A warm qwen2.5:1.5b answers in ~2s on CPU
// (measured: 50 generated tokens at 16 tok/s); 20s covers a cold model load
// without holding a connection for a minute. The HTTP server's own write timeout
// is 30s.
const DefaultTimeout = 20 * time.Second

// KeepAlive is what makes this endpoint usable at all. Ollama unloads a model
// after 5 minutes by default, and reloading one costs ~6s on this CPU; holding
// it for 30 minutes turns the first click into the slow one and every later
// click into a ~2s one.
const KeepAlive = "30m"

// Alternatives is how many rephrasings to ask for, and numPredict the token
// budget that allows them. Two is deliberate: measured on this CPU, one
// alternative takes ~1s and three take ~3.2s, and the third is usually the
// weakest or a verbatim echo of the input (which gets filtered out below).
const (
	Alternatives = 2
	numPredict   = 70
	temperature  = 0.3 // a rewrite should not be creative
)

// Sentinels the API layer turns into user-facing errors: they name different
// fixes (start Ollama, pull the model, pick a lighter model).
var (
	ErrUnavailable  = errors.New("rewrite backend unavailable")
	ErrModelMissing = errors.New("rewrite model not installed")
	ErrTimeout      = errors.New("rewrite timed out")
)

// Client talks to one Ollama model.
type Client struct {
	URL     string // e.g. http://127.0.0.1:11434
	Model   string // e.g. qwen2.5:1.5b
	Timeout time.Duration
	HTTP    *http.Client
}

// New returns a client for url and model. Neither is checked here: an Ollama
// that is down must not stop the server from starting.
func New(url, model string) *Client {
	return &Client{URL: url, Model: model}
}

type generateRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Stream    bool           `json:"stream"`
	KeepAlive string         `json:"keep_alive"`
	Options   map[string]any `json:"options,omitempty"`
}

type generateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error"`
}

// Rewrite returns rephrased versions of text. tone and intent are optional
// hints ("formal", "concise") and are left out of the prompt when empty.
func (c *Client) Rewrite(ctx context.Context, text, tone, intent string) ([]string, error) {
	body, err := json.Marshal(generateRequest{
		Model:     c.Model,
		Prompt:    buildPrompt(text, tone, intent),
		Stream:    false, // we parse one JSON body, not a stream of them
		KeepAlive: KeepAlive,
		Options:   map[string]any{"num_predict": numPredict, "temperature": temperature},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.URL, "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client().Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return nil, fmt.Errorf("%w after %s (the model may still be loading)", ErrTimeout, c.timeout())
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading reply: %v", ErrUnavailable, err)
	}
	var out generateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %s answered %d with %q", ErrUnavailable, c.URL, resp.StatusCode, truncate(string(raw), 120))
	}
	if out.Error != "" {
		low := strings.ToLower(out.Error)
		if strings.Contains(low, "not found") || strings.Contains(low, "no such model") {
			return nil, fmt.Errorf("%w: %s", ErrModelMissing, out.Error)
		}
		return nil, fmt.Errorf("rewrite backend error: %s", out.Error)
	}

	return candidates(out.Response, text), nil
}

// WarmUp loads the model so the first real click does not pay for it. Best
// effort by design: with Ollama down this returns an error the caller logs and
// ignores, because nothing else in the server depends on it.
func (c *Client) WarmUp(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // a cold load is slow
	defer cancel()
	_, err := c.Rewrite(ctx, "We should schedule a meeting in order to discuss the report.", "", "")
	return err
}

// buildPrompt keeps the instruction to one sentence. Prefill is the second cost
// after model load (67 prompt tokens here, 0.09s) and a small model uses none of
// a long instruction.
//
// Measured on qwen2.5:1.5b, same sentence either way:
//
//	no hints    "Because it was late, we used the car to drive home."   (good)
//	tone=formal "Because of the late hour, we employed the car for the
//	             purpose of returning home."                           (padded)
//
// Asking a 1.5B model for a tone makes it add words rather than fix them, and
// adding "do not add words" to the prompt did not change that. Leave both hints
// empty for the best rewrite; prefer intent=concise when one is needed.
func buildPrompt(text, tone, intent string) string {
	var b strings.Builder
	b.WriteString("Rephrase this sentence")
	if intent != "" {
		fmt.Fprintf(&b, " to be more %s", intent)
	}
	if tone != "" {
		fmt.Fprintf(&b, " in a %s tone", tone)
	}
	fmt.Fprintf(&b, ". Keep the meaning. Reply with %d alternatives, one per line, no numbering, no commentary.\n\n", Alternatives)
	b.WriteString(text)
	return b.String()
}

// candidates splits the model's reply into usable alternatives: numbering and
// bullets stripped, blanks dropped, an echo of the original dropped (a small
// model likes to return the input unchanged, which is not a suggestion).
func candidates(reply, original string) []string {
	orig := strings.ToLower(strings.TrimSpace(original))
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•)0123456789. "))
		if line == "" {
			continue
		}
		key := strings.ToLower(line)
		if key == orig || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
		if len(out) == Alternatives {
			break
		}
	}
	return out
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: c.timeout()}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
