// Package ollama is a minimal client for the Ollama API.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client talks to a local Ollama instance.
type Client struct {
	BaseURL string
	Model   string
	cli     *http.Client
}

func New(baseURL, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		cli:     &http.Client{Timeout: 60 * time.Second},
	}
}

// Rephrase asks the model to rewrite the given text.
func (c *Client) Rephrase(ctx context.Context, text string) (string, error) {
	prompt := fmt.Sprintf(
		"Rewrite the following sentence to fix grammar, spelling, and style issues while preserving the meaning. Return ONLY the rewritten sentence with no explanation.\n\nSentence: %s\n\nRewritten:", text)

	body := map[string]any{
		"model":  c.Model,
		"prompt": prompt,
		"stream": false,
		"think":  false,
		"options": map[string]any{
			"temperature": 0.3,
			"num_predict": 128,
		},
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/generate", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ollama decode: %w", err)
	}
	return out.Response, nil
}
