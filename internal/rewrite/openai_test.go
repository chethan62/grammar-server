package rewrite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeOpenAI is a backend that speaks the /v1 shape: the request it received is
// captured so the test can assert on the shape, not just the answer.
func fakeOpenAI(t *testing.T, status int, reply string) (*httptest.Server, *chatRequest) {
	t.Helper()
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "" && !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Authorization = %q, want a Bearer token", auth)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestOpenAIRewriteSendsTheExpectedShape(t *testing.T) {
	srv, got := fakeOpenAI(t, 200, `{"choices":[{"message":{"content":"Because it was late, we drove home.\nWe drove home because it was late."}}]}`)
	c := &OpenAI{URL: srv.URL, Model: "qwen2.5-1.5b-instruct", APIKey: "sk-test", HTTP: srv.Client()}

	got2, err := c.Rewrite(context.Background(), "Because it was late, we used the car to drive home.", "", "concise")
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	if got.Model != "qwen2.5-1.5b-instruct" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Stream {
		t.Error("stream = true; this client parses one JSON body")
	}
	if got.Temperature != temperature {
		t.Errorf("temperature = %v, want %v (a rewrite must not be creative)", got.Temperature, temperature)
	}
	if got.MaxTokens != tokenBudget {
		t.Errorf("max_tokens = %d, want %d", got.MaxTokens, tokenBudget)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system instruction + the sentence)", len(got.Messages))
	}
	if got.Messages[0].Role != "system" || !strings.Contains(got.Messages[0].Content, "one per line") {
		t.Errorf("first message = %q/%q, want the shared instruction", got.Messages[0].Role, got.Messages[0].Content)
	}
	if !strings.Contains(got.Messages[0].Content, "more concise") {
		t.Error("the intent hint did not reach the instruction")
	}
	if got.Messages[1].Role != "user" || !strings.HasPrefix(got.Messages[1].Content, "Because it was late") {
		t.Errorf("second message = %q/%q, want the text", got.Messages[1].Role, got.Messages[1].Content)
	}

	if len(got2) != 2 {
		t.Fatalf("candidates = %v, want 2", got2)
	}
	if got2[0] != "Because it was late, we drove home." {
		t.Errorf("first candidate = %q", got2[0])
	}
}

// A local server asks for no key, and sending "Bearer " with nothing after it
// makes some of them answer 401.
func TestOpenAIRewriteWithoutKeySendsNoAuthorization(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Shorter."}}]}`))
	}))
	defer srv.Close()
	c := &OpenAI{URL: srv.URL, Model: "m", HTTP: srv.Client()}
	if _, err := c.Rewrite(context.Background(), "One long sentence here.", "", ""); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want none for a local server", auth)
	}
}

// Each failure has to name a different fix, so each has to be a different
// sentinel: "install the model" and "set the key" are not the same problem.
func TestOpenAIRewriteErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unknown model", 404, `{"error":{"message":"model 'nope' not found"}}`, ErrModelMissing},
		{"bad key", 401, `{"error":{"message":"invalid api key"}}`, ErrAuth},
		{"forbidden", 403, `{"error":{"message":"no access to that model"}}`, ErrAuth},
		{"busy", 429, `{"error":{"message":"slow down"}}`, ErrUnavailable},
		{"html from a proxy", 502, `<html>bad gateway</html>`, ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeOpenAI(t, tc.status, tc.body)
			c := &OpenAI{URL: srv.URL, Model: "nope", HTTP: srv.Client()}
			_, err := c.Rewrite(context.Background(), "Text.", "", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A 200 that is not the right shape must not be reported as a successful
// rewrite with zero candidates: that is what a stopped backend behind a
// catch-all proxy looks like.
func TestOpenAIRewriteRejectsANonChatBody(t *testing.T) {
	srv, _ := fakeOpenAI(t, 200, `<html>hello</html>`)
	c := &OpenAI{URL: srv.URL, Model: "m", HTTP: srv.Client()}
	_, err := c.Rewrite(context.Background(), "Text.", "", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestOpenAIRewriteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	// No HTTP field: an injected client would carry its own timeout and the
	// request would never time out, which is the trap this test exists to catch.
	c := &OpenAI{URL: srv.URL, Model: "m", Timeout: 30 * time.Millisecond}
	_, err := c.Rewrite(context.Background(), "Text.", "", "")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestOpenAIRewriteUnreachable(t *testing.T) {
	// Nothing listens here: closing a server and reusing its URL is the cheapest
	// reliable "connection refused".
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := &OpenAI{URL: url, Model: "m", Timeout: time.Second}
	_, err := c.Rewrite(context.Background(), "Text.", "", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5:1.5b"},{"id":"llama3.2:3b"},{"id":""}]}`))
	}))
	defer srv.Close()

	got, err := ListModels(context.Background(), srv.URL+"/", "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"llama3.2:3b", "qwen2.5:1.5b"} // sorted, blank dropped
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

func TestListModelsUnreachableIsAnErrorNotAPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := ListModels(context.Background(), url, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestBuildPicksTheRightClient(t *testing.T) {
	if r := Build("ollama", "http://127.0.0.1:11434", "qwen2.5:1.5b", ""); r == nil {
		t.Fatal("ollama: got nil")
	} else if _, ok := r.(*Client); !ok {
		t.Fatalf("ollama: got %T, want *Client (native /api/generate, keep_alive)", r)
	}
	for _, id := range []string{"llamacpp", "lmstudio", "vllm", "openrouter", "openai"} {
		r := Build(id, "http://127.0.0.1:1234", "m", "")
		if r == nil {
			t.Fatalf("%s: got nil", id)
		}
		if _, ok := r.(*OpenAI); !ok {
			t.Fatalf("%s: got %T, want *OpenAI", id, r)
		}
	}
	// A half-configured rewrite is nil, which every caller treats as "no rewrite".
	for _, bad := range []struct{ provider, url, model string }{
		{"nonsense", "http://x", "m"},
		{"ollama", "", "m"},
		{"ollama", "http://x", ""},
	} {
		if r := Build(bad.provider, bad.url, bad.model, ""); r != nil {
			t.Errorf("Build(%q,%q,%q) = %T, want nil", bad.provider, bad.url, bad.model, r)
		}
	}
}

func TestIDForURLGuessesTheProduct(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:11434":       "ollama",
		"http://localhost:11434/":      "ollama",
		"http://127.0.0.1:1234":        "lmstudio",
		"http://127.0.0.1:8080":        "llamacpp",
		"http://127.0.0.1:8000":        "vllm",
		"https://openrouter.ai/api/v1": "openrouter",
		"http://192.168.29.50:11434":   "ollama", // port is the reliable signal
		"http://192.168.29.50:9999/v1": "openai", // unknown: the shape that fits most
	}
	for url, want := range cases {
		if got := IDForURL(url); got != want {
			t.Errorf("IDForURL(%q) = %q, want %q", url, got, want)
		}
	}
}

// Every preset must be constructible, or the UI offers a button that cannot work.
func TestPresetsAreUsable(t *testing.T) {
	if len(Presets) < 4 {
		t.Fatalf("only %d presets", len(Presets))
	}
	for _, p := range Presets {
		if p.ID == "" || p.Label == "" || p.Protocol == "" {
			t.Errorf("preset %+v is missing an id, label or protocol", p)
		}
		if p.Protocol != ProtoOllama && p.Protocol != ProtoOpenAI {
			t.Errorf("preset %s: protocol %q is neither", p.ID, p.Protocol)
		}
		if _, ok := Resolve(p.ID); !ok {
			t.Errorf("preset %s does not resolve to itself", p.ID)
		}
		// Everything local but the catch-all must have a default URL to show.
		if p.Local && p.ID != "openai" && p.URL == "" {
			t.Errorf("preset %s is local but has no default URL", p.ID)
		}
	}
	if _, ok := Resolve("OLLAMA  "); !ok {
		t.Error("Resolve should trim and lowercase")
	}
}
