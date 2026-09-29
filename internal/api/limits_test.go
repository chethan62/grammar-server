package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// An unknown language used to be checked as American English: HTTP 200, code
// "de-DE", zero matches — a German document reported as clean. LanguageTool
// answers an unknown code with 400 and a plain-text body listing the codes it
// knows; this pins the refusal and the list.
func TestUnknownLanguageIsRefused(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/v2/check", "application/json",
		strings.NewReader(`{"text":"Das ist ein Satz.","language":"de-DE"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "Error: 'de-DE' is not a language code known to grammar-server.") {
		t.Errorf("body = %q, want the LanguageTool-shaped language error", body)
	}
	if !strings.Contains(string(body), "en-GB") {
		t.Errorf("body must list the supported codes: %q", body)
	}
}

// GET /v2/languages and the check path share one table: every advertised code
// must actually be checkable, and the advertised names must not be the codes.
func TestLanguagesComeFromTheTable(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v2/languages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var langs []struct {
		Name     string `json:"name"`
		Code     string `json:"code"`
		LongCode string `json:"longCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&langs); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range langs {
		seen[l.Code] = true
		if l.Name == "" || l.Name == l.Code {
			t.Errorf("entry %+v: name is empty or is just the code", l)
		}
		// longCode is not decoration: language_tool_python's _get_languages()
		// adds e.get("code") and e.get("longCode") for every entry, then
		// lowercases each member — a missing key adds None and the client dies
		// before its first check.
		if l.LongCode == "" {
			t.Errorf("entry %+v: longCode is missing (LanguageTool clients read it)", l)
		}
		// A code we advertise must survive a real check.
		body := `{"text":"He go to the store.","language":"` + l.Code + `"}`
		r, err := http.Post(srv.URL+"/v2/check", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("check with advertised language %q = %d, want 200", l.Code, r.StatusCode)
		}
	}
	for _, want := range []string{"en", "en-US", "en-GB", "en-CA", "en-AU", "en-IN"} {
		if !seen[want] {
			t.Errorf("/v2/languages is missing %q", want)
		}
	}
}

// /v2/stats is arithmetic over the string: no engine is started, so the numbers
// do not depend on harper and repeat exactly.
func TestStatsEndpoint(t *testing.T) {
	srv := newTestServer(t)
	text := "The cat sat. Do you not agree? Yes indeed, the well-known cat sat on the mat!"

	resp, err := http.Post(srv.URL+"/v2/stats", "application/json",
		strings.NewReader(`{"text":`+strconv.Quote(text)+`,"language":"en-US"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var st struct {
		Words               int     `json:"words"`
		Sentences           int     `json:"sentences"`
		UniqueWords         int     `json:"uniqueWords"`
		UniqueRatio         float64 `json:"uniqueRatio"`
		MedianSentenceWords float64 `json:"medianSentenceWords"`
		ReadingTime         string  `json:"readingTime"`
		Grade               string  `json:"grade"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Words != 16 || st.Sentences != 3 || st.UniqueWords != 12 {
		t.Errorf("counts = %+v, want words 16, sentences 3, unique 12", st)
	}
	if st.UniqueRatio != 0.75 || st.MedianSentenceWords != 4 {
		t.Errorf("ratios = %+v, want uniqueRatio 0.75, median 4", st)
	}
	if st.ReadingTime != "4 sec" || st.Grade == "" {
		t.Errorf("readingTime = %q, grade = %q", st.ReadingTime, st.Grade)
	}

	// The scores are English-only, so an unknown language is refused here too.
	r2, err := http.Post(srv.URL+"/v2/stats", "application/json",
		strings.NewReader(`{"text":"Das ist ein Satz.","language":"de-DE"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("stats with language de-DE = %d, want 400", r2.StatusCode)
	}

	// No text at all is a client error, not a page of zeroes.
	r3, err := http.Post(srv.URL+"/v2/stats", "application/json", strings.NewReader(`{"text":""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusBadRequest {
		t.Errorf("empty text = %d, want 400", r3.StatusCode)
	}
}

// Both size guards on every text endpoint. The declared-length one runs first and
// returns LanguageTool's shape: when the body reader wins instead, the decoder's
// JSON 400 "request body too large" is what a client sees, which is not the error
// LanguageTool defines. /v2/rewrite had exactly that bug, /v2/fix-sentence still
// did, and the guard now lives in one place (capBody/textTooLong) for all of them.
func TestBothSizeGuardsOnEveryTextEndpoint(t *testing.T) {
	srv := newTestServer(t)
	paths := []struct{ path, body string }{
		{"/v2/check", `{"text":"hi","language":"en-US"}`},
		{"/v2/fix-sentence", `{"text":"hi","offset":0}`},
		{"/v2/stats", `{"text":"hi","language":"en-US"}`},
	}

	// Declared over the cap, sent small: the server must refuse on Content-Length
	// without reading the body. Driven through the handler because net/http's
	// client refuses to send a body shorter than the Content-Length it was given.
	for _, tc := range paths {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = 5 << 20 // declared 5 MB
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s declared 5 MB: status = %d, want 413", tc.path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s declared 5 MB: content-type = %q, want text/plain", tc.path, ct)
		}
		if !strings.HasPrefix(rec.Body.String(), "Error: ") {
			t.Errorf("%s declared 5 MB: body = %q, want LanguageTool's error prefix", tc.path, rec.Body.String())
		}
	}

	// Honest length over the character cap: refused by the rune count, which can
	// name the size it got because the body was small enough to read.
	big := strings.Repeat("a", maxTextCharsTest+1000)
	for _, tc := range paths {
		body := strings.Replace(tc.body, `"hi"`, strconv.Quote(big), 1)
		resp, err := http.Post(srv.URL+tc.path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s over the cap: status = %d, want 413", tc.path, resp.StatusCode)
		}
		if !strings.Contains(string(got), "it's 201000 characters") {
			t.Errorf("%s over the cap: body = %q, want the actual size named", tc.path, got)
		}
	}
}

// maxTextCharsTest mirrors api.maxTextChars, which an external test package cannot
// read. Keep it in step if the cap moves.
const maxTextCharsTest = 200_000
