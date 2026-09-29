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
	"sort"
	"strings"
	"time"
)

// OpenAI talks to any server speaking the /v1/chat/completions shape: llama.cpp
// server, LM Studio, vLLM, OpenRouter, Ollama's own /v1 shim, and everything
// else that copied the API. That is why one file covers four of the six presets
// — the difference between them is a base URL and a model name.
type OpenAI struct {
	URL     string // base, e.g. http://127.0.0.1:1234 — no /v1 suffix needed
	Model   string
	APIKey  string // empty for local servers, which do not ask
	Timeout time.Duration
	HTTP    *http.Client
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Rewrite asks the backend for rephrasings. The instruction goes in a system
// message and the sentence in a user message, which is the only difference from
// the Ollama client — both send the same words.
func (c *OpenAI) Rewrite(ctx context.Context, text, tone, intent string) ([]string, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []message{
			{Role: "system", Content: instruction(tone, intent)},
			{Role: "user", Content: text},
		},
		Temperature: temperature,
		MaxTokens:   tokenBudget,
		Stream:      false,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint()+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.client().Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return nil, fmt.Errorf("%w after %s (a cold model is slow, a bigger one slower)", ErrTimeout, c.timeout())
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading reply: %v", ErrUnavailable, err)
	}

	// Status first: an OpenAI-compatible server sends its errors as a JSON body
	// with an `error` object, but a proxy in front of it may send HTML, and both
	// need to come out as something a user can act on.
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s does not have model %q — %s/v1/models lists what it has",
			ErrModelMissing, c.URL, c.Model, c.endpoint())
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrAuth, resp.StatusCode, apiErrorText(raw))
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: rate limited by %s", ErrUnavailable, c.URL)
	default:
		return nil, fmt.Errorf("%w: %s answered %d: %s", ErrUnavailable, c.URL, resp.StatusCode, apiErrorText(raw))
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %s answered 200 with %q (not the /v1/chat/completions shape?)",
			ErrUnavailable, c.URL, truncate(string(raw), 120))
	}
	if out.Error != nil && out.Error.Message != "" {
		low := strings.ToLower(out.Error.Message)
		if strings.Contains(low, "not found") || strings.Contains(low, "no such") || strings.Contains(low, "unknown model") {
			return nil, fmt.Errorf("%w: %s", ErrModelMissing, out.Error.Message)
		}
		return nil, fmt.Errorf("rewrite backend error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: %s returned no choices", ErrUnavailable, c.URL)
	}

	return candidates(out.Choices[0].Message.Content, text), nil
}

// ListModels asks the backend what it has. Every preset here serves /v1/models
// — Ollama included — so one call fills the UI's model list for all of them.
// It is advisory: a backend that does not answer still works if you type a name.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("%w: no URL", ErrUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	// Short: this runs while a page is loading and an unreachable backend must
	// not hold the settings panel for the default 20s.
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered %d for /v1/models", ErrUnavailable, base, resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, truncate(string(raw), 80))
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// apiErrorText pulls the message out of an OpenAI-shaped error body, falling
// back to the raw text so a non-JSON answer still says something useful.
func apiErrorText(raw []byte) string {
	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && out.Error.Message != "" {
		return out.Error.Message
	}
	return truncate(strings.TrimSpace(string(raw)), 160)
}

func (c *OpenAI) endpoint() string { return strings.TrimSuffix(c.URL, "/") }

func (c *OpenAI) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *OpenAI) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: c.timeout()}
}
