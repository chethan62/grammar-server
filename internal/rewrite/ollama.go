// Ollama's native API. This file is the one place that speaks /api/generate
// rather than the /v1 shape, and it earns its keep with keep_alive: Ollama is
// the only backend here that can hold a model in memory between clicks.
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

// KeepAlive is what makes this endpoint usable at all. Ollama unloads a model
// after 5 minutes by default, and reloading one costs ~6s on this CPU; holding
// it for 30 minutes turns the first click into the slow one and every later
// click into a ~2s one.
const KeepAlive = "30m"

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
		Options:   map[string]any{"num_predict": tokenBudget, "temperature": temperature},
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

// buildPrompt is Ollama's single-prompt shape: the shared instruction, a blank
// line, then the sentence. The OpenAI client sends the same two parts as two
// messages instead, which is the only difference between the clients.
func buildPrompt(text, tone, intent string) string {
	return instruction(tone, intent) + "\n\n" + text
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
