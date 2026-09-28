package api_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An oversized body must be refused fast, in LanguageTool's own error shape.
// Before the cap existed this text was handed to harper-ls: a 200 KB document
// ran past 47 s and kept the engine at 96% CPU after the client gave up.
//
// LanguageTool's public API answers 35,000 characters with:
//
//	HTTP/2 413
//	Error: Your text exceeds the limit of 20000 characters (it's 35000 characters). Please submit a shorter text.
func TestTextTooLargeIsRefused(t *testing.T) {
	srv := newTestServer(t)
	big := strings.Repeat("This is a perfectly fine sentence. ", 8000) // ≈ 280 KB

	body := `{"text":` + strconv.Quote(big) + `,"language":"en-US"}`
	start := time.Now()
	resp, err := http.Post(srv.URL+"/v2/check", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain (LanguageTool's error shape)", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	want := "Error: Your text exceeds the limit of 200000 characters"
	if !strings.HasPrefix(string(got), want) {
		t.Errorf("body = %q, want a prefix of %q", got, want)
	}
	if elapsed > 2*time.Second {
		t.Errorf("refusal took %s — the engine was reached", elapsed)
	}
}
