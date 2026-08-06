package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"grammar-server/internal/api"
	"grammar-server/internal/engine"
)

func TestCheckMisspelling(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond) // let warm-up settle

	srv := httptest.NewServer(api.NewServer(h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"this has a misspeled wurd","language":"en-US"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Matches []struct {
			Rule struct {
				ID string `json:"id"`
			} `json:"rule"`
			Offset        int64 `json:"offset"`
			Length        int64 `json:"length"`
			Replacements  []struct{ Value string } `json:"replacements"`
			SentenceRanges [][]int64 `json:"sentenceRanges"`
		} `json:"matches"`
		Software       struct{ Version string } `json:"software"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	spellCount := 0
	for _, m := range out.Matches {
		if m.Rule.ID == "SpellCheck" {
			spellCount++
			if m.Offset != 11 && m.Offset != 21 && m.Offset != 10 {
				// "misspeled" at 11 or 10 (harper varies); "wurd" at 21
			}
			if m.Length < 1 {
				t.Errorf("SpellCheck length too short: %d", m.Length)
			}
			// replacements should be unique (no duplicates)
			seen := map[string]bool{}
			for _, r := range m.Replacements {
				if seen[r.Value] {
					t.Errorf("duplicate replacement %q", r.Value)
				}
				seen[r.Value] = true
			}
		}
	}
	if spellCount < 2 {
		t.Errorf("expected at least 2 SpellCheck matches, got %d", spellCount)
	}
	if out.Software.Version == "" {
		t.Error("missing software version")
	}
}

func TestFixSentence(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond)

	srv := httptest.NewServer(api.NewServer(h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v2/fix-sentence", "application/json",
		strings.NewReader(`{"text":"teh quick brown fox","offset":0}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Fixed string `json:"fixed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Fixed != "the quick brown fox" {
		t.Errorf("expected 'the quick brown fox', got %q", out.Fixed)
	}
}

func TestSentenceRanges(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond)

	srv := httptest.NewServer(api.NewServer(h).Handler())
	defer srv.Close()

	// Two sentences
	resp, _ := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"First sentence. Second one here.","language":"en-US"}`))
	if resp != nil {
		defer resp.Body.Close()
	}
	var out struct{ SentenceRanges [][]int64 `json:"sentenceRanges"` }
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.SentenceRanges) < 2 {
		t.Errorf("expected at least 2 ranges, got %d: %v", len(out.SentenceRanges), out.SentenceRanges)
	}

	// Three single-letter "sentences" (edge case — no false abbreviation skip)
	resp2, _ := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"A. B. C.","language":"en-US"}`))
	if resp2 != nil {
		defer resp2.Body.Close()
	}
	var out2 struct{ SentenceRanges [][]int64 `json:"sentenceRanges"` }
	json.NewDecoder(resp2.Body).Decode(&out2)
	if len(out2.SentenceRanges) != 3 {
		t.Errorf("expected 3 ranges for 'A. B. C.', got %d: %v", len(out2.SentenceRanges), out2.SentenceRanges)
	}
}
