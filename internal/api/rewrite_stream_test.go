package api_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"grammar-server/internal/api"
)

// fakeOllamaStream is a backend that answers the way the streaming protocol does: one JSON object per
// write, the last one carrying done.
func fakeOllamaStream(t *testing.T, pieces ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		for _, piece := range pieces {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": piece, "done": false})
			if flusher != nil {
				flusher.Flush()
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "", "done": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The streamed endpoint: the words as they arrive, then the same body the unstreamed call returns.
//
// A client has to be able to tell the two apart by which key is present rather than by position,
// because the alternative — counting lines — breaks the moment a backend ignores `stream` and answers
// in one body. Both halves of that promise are checked here.
func TestRewriteStreamsWhenAsked(t *testing.T) {
	srv := rewriteServer(t, fakeOllamaStream(t, "We are ", "formulating", " the report.").URL, "m")

	resp, err := http.Post(srv.URL+"/v2/rewrite", "application/json",
		strings.NewReader(`{"text":"We are zorbulating the report.","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v2/rewrite (stream) = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson so a client knows to read it as a stream", ct)
	}

	var sent string
	var final api.RewriteResponse
	lines := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		lines++
		var line map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", lines, err, scanner.Text())
		}
		if delta, ok := line["delta"]; ok {
			var piece string
			_ = json.Unmarshal(delta, &piece)
			sent += piece
			continue
		}
		if _, ok := line["candidates"]; ok {
			if err := json.Unmarshal(scanner.Bytes(), &final); err != nil {
				t.Fatalf("the last line is not a rewrite response: %v", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if sent != "We are formulating the report." {
		t.Errorf("the deltas add up to %q, want the model's words", sent)
	}
	if len(final.Candidates) != 1 || final.Candidates[0] != "We are formulating the report." {
		t.Errorf("candidates = %q, want the same answer the unstreamed call gives", final.Candidates)
	}
	if final.Model != "m" || final.Provider != "ollama" {
		t.Errorf("the last line should say what answered: model=%q provider=%q", final.Model, final.Provider)
	}
}

// The promise that makes streaming safe to ask for unconditionally: whatever the backend does — stream
// one body or several — the reply is still lines a streaming client can read, and the answer is always
// in the last one. This is the contract the card's reader depends on, so it is the contract checked.
func TestAskingToStreamNeverBreaksTheCall(t *testing.T) {
	// fakeOllama answers one JSON body whatever the request says: a backend that ignores `stream`.
	srv := rewriteServer(t, fakeOllama(t, "We are formulating the report.").URL, "m")

	resp, err := http.Post(srv.URL+"/v2/rewrite", "application/json",
		strings.NewReader(`{"text":"We are zorbulating the report.","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("= %d, want 200 (%s)", resp.StatusCode, body)
	}

	var last api.RewriteResponse
	lines := 0
	for _, raw := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		lines++
		var kind map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &kind); err != nil {
			t.Fatalf("line %d is not JSON: %q", lines, raw)
		}
		_, isDelta := kind["delta"]
		_, isMessage := kind["message"]
		_, isAnswer := kind["candidates"]
		if !isDelta && !isMessage && !isAnswer {
			t.Fatalf("line %d is none of delta/message/candidates, so a client cannot tell what it "+
				"is: %q", lines, raw)
		}
		if isAnswer {
			if err := json.Unmarshal([]byte(raw), &last); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(last.Candidates) != 1 || last.Candidates[0] != "We are formulating the report." {
		t.Errorf("the last line = %+v, want one body's answer in it", last)
	}
}

// A failure after the stream has started cannot be a status code — the status line is long gone — so
// it travels as a line, and the client has to be able to see it.
func TestAStreamedFailureArrivesAsAMessage(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := down.URL
	down.Close() // nothing is listening now
	srv := rewriteServer(t, url, "m")

	resp, err := http.Post(srv.URL+"/v2/rewrite", "application/json",
		strings.NewReader(`{"text":"We are zorbulating the report.","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var line map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &line); err != nil {
		t.Fatalf("the reply is not one JSON line (%q): %v", body, err)
	}
	if line["message"] == "" {
		t.Fatalf("a failed stream should carry a message, got %q", body)
	}
	if !strings.Contains(line["message"], "unavailable") {
		t.Errorf("the message should name the fault in the usual words: %q", line["message"])
	}
}
