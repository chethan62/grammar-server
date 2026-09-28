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

// The fake backend records the request, so the contract is pinned where it
// matters: the model, keep_alive (5 minutes of default Ollama behaviour is what
// makes the second click as slow as the first), stream=false, and a prompt that
// carries the sentence and the tone hints.
func TestRewriteSendsTheContractAndParsesCandidates(t *testing.T) {
	var got generateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("path = %s, want /api/generate", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(generateResponse{
			Response: "1. To decide on the meeting, we should schedule it soon.\n" +
				"2) We should schedule the meeting soon to make a decision.\n" +
				"* In order to schedule a meeting, we should decide soon.\n",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "qwen2.5:1.5b")
	got2, err := c.Rewrite(context.Background(), "In order to make a decision, we should schedule a meeting.", "formal", "concise")
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != Alternatives {
		t.Fatalf("candidates = %q, want %d", got2, Alternatives)
	}
	if got2[0] != "To decide on the meeting, we should schedule it soon." {
		t.Errorf("first candidate = %q (numbering not stripped?)", got2[0])
	}
	if got2[1] != "We should schedule the meeting soon to make a decision." {
		t.Errorf(`second candidate = %q ("2)" not stripped?)`, got2[1])
	}

	if got.Model != "qwen2.5:1.5b" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Stream {
		t.Error("stream must be false: the reply is parsed as one JSON body")
	}
	if got.KeepAlive != KeepAlive {
		t.Errorf("keep_alive = %q, want %q", got.KeepAlive, KeepAlive)
	}
	for _, want := range []string{"In order to make a decision", "formal", "concise", "2 alternatives"} {
		if !strings.Contains(got.Prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got.Prompt)
		}
	}
}

// A small model likes to return the input unchanged. That is not a suggestion
// and must never reach a user as one.
func TestEchoAndBlanksAreDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(generateResponse{
			Response: "We should schedule a meeting.\n\n" + // exact echo
				"we should SCHEDULE a meeting.\n" + // case-only difference
				"- We should book a meeting.\n" +
				"- We should book a meeting.\n", // duplicate
		})
	}))
	defer srv.Close()

	got, err := New(srv.URL, "m").Rewrite(context.Background(), "We should schedule a meeting.", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "We should book a meeting." {
		t.Errorf("candidates = %q, want just the new alternative", got)
	}
}

func TestBackendDownIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	_, err := New(url, "m").Rewrite(context.Background(), "text", "", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestMissingModelIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(generateResponse{Error: `model "nope:7b" not found, try pulling it first`})
	}))
	defer srv.Close()

	_, err := New(srv.URL, "nope:7b").Rewrite(context.Background(), "text", "", "")
	if !errors.Is(err, ErrModelMissing) {
		t.Fatalf("err = %v, want ErrModelMissing", err)
	}
}

// A slow backend must not hold the request: the deadline is the client's, and
// the error says so, because "timed out" and "not running" need different fixes.
func TestSlowBackendTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(generateResponse{Response: "late"})
	}))
	defer srv.Close()
	defer close(release)

	c := New(srv.URL, "m")
	c.Timeout = 100 * time.Millisecond

	start := time.Now()
	_, err := c.Rewrite(context.Background(), "text", "", "")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s, want ~100ms", d)
	}
}

// The prompt must not ask for alternatives it will not use, and the count in the
// prompt is what the request body is checked against, so a drifting constant
// fails here rather than in production.
func TestPromptAsksForTheConfiguredCount(t *testing.T) {
	p := buildPrompt("Sentence.", "", "")
	if !strings.HasSuffix(p, "\n\nSentence.") {
		t.Errorf("prompt must end with the sentence, got %q", p)
	}
	if !strings.Contains(p, "2 alternatives") {
		t.Errorf("prompt = %q", p)
	}
	if strings.Contains(buildPrompt("S.", "formal", ""), "concise") {
		t.Error("an empty intent must not appear in the prompt")
	}
}
