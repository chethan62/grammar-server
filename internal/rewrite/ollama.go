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
	return c.rewrite(ctx, text, tone, intent, nil)
}

// RewriteStream is Rewrite with the answer handed over while it is written.
//
// Ollama's /api/generate streams one JSON object per line, so a caller can show words as the model
// produces them instead of waiting for the whole sentence. The candidates returned are exactly what
// Rewrite returns, parsed from the accumulated text: the deltas are a progress channel, not a
// different answer, so a caller's final handling is the same either way.
//
// A cancelled context — the client went away, or pressed Cancel — reaches Ollama and stops the
// generation, which is the only cancel path this needs.
func (c *Client) RewriteStream(ctx context.Context, text, tone, intent string,
	onDelta func(string)) ([]string, error) {
	if onDelta == nil {
		onDelta = func(string) {}
	}
	return c.rewrite(ctx, text, tone, intent, onDelta)
}

// rewrite is the one place that speaks /api/generate: Rewrite and RewriteStream differ only in
// whether the reply is parsed as one body or as it arrives.
func (c *Client) rewrite(ctx context.Context, text, tone, intent string,
	onDelta func(string)) ([]string, error) {
	body, err := json.Marshal(generateRequest{
		Model:     c.Model,
		Prompt:    buildPrompt(text, tone, intent),
		Stream:    onDelta != nil, // one JSON object per line rather than a single body
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

	if onDelta != nil {
		return c.collect(resp, text, onDelta)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading reply: %v", ErrUnavailable, err)
	}
	var out generateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %s answered %d with %q", ErrUnavailable, c.URL, resp.StatusCode, truncate(string(raw), 120))
	}
	if out.Error != "" {
		return nil, backendError(out.Error)
	}

	return candidates(out.Response, text), nil
}

// collect reads a streamed reply: JSON objects one after another, whitespace between them, the last
// carrying done. A decoder rather than a line scanner, because newline-delimited JSON is still JSON
// and this way the framing does not depend on how Ollama chooses to separate them.
func (c *Client) collect(resp *http.Response, text string, onDelta func(string)) ([]string, error) {
	var full strings.Builder
	finished := false
	decoder := json.NewDecoder(resp.Body)
	for !finished {
		var chunk generateResponse
		if err := decoder.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%w: reading stream: %v", ErrUnavailable, err)
		}
		if chunk.Error != "" {
			return nil, backendError(chunk.Error)
		}
		if chunk.Response != "" {
			full.WriteString(chunk.Response)
			onDelta(chunk.Response)
		}
		finished = chunk.Done
	}
	if !finished {
		// A stream that stops without `done` means the model was cut off: the backend reloaded, or the
		// connection to it dropped. What arrived is half a sentence, and offering that as a rewrite is
		// the quiet kind of lie this file avoids elsewhere — the client's own reader calls this shape
		// "ended without an answer" and fails on it. So does this, and it says how far it got.
		//
		// Ollama sends done even when it stops at the token limit, so a missing done is never a
		// finished answer that happens to be short.
		return nil, fmt.Errorf("%w: the model stopped %d characters in, without finishing",
			ErrUnavailable, full.Len())
	}
	return candidates(full.String(), text), nil
}

// backendError is what the backend's own complaint means, in the terms the rest of this package
// reports: a missing model is a thing the user can fix, anything else is the backend failing.
func backendError(message string) error {
	low := strings.ToLower(message)
	if strings.Contains(low, "not found") || strings.Contains(low, "no such model") {
		return fmt.Errorf("%w: %s", ErrModelMissing, message)
	}
	return fmt.Errorf("rewrite backend error: %s", message)
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
