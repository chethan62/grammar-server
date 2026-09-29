package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grammar-server/internal/engine"
	"grammar-server/internal/rewrite"
)

// newTestServer builds the smallest server that can answer /v1/ai. The engine is
// nil because nothing on this path touches it.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	// The choice is saved under XDG_CONFIG_HOME, so every test gets its own
	// directory. Without this a test would rewrite the real setting of whoever
	// runs it, which is the kind of bug that shows up as "my UI forgot my model".
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return NewServer((*engine.Harper)(nil))
}

// fakeBackend serves the two /v1 routes of an OpenAI-compatible server.
func fakeBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-1.5b-instruct"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Shorter version."}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getAI(t *testing.T, s *Server) AIState {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ai", nil)
	req.RemoteAddr = "127.0.0.1:41234" // httptest defaults to a documentation address, which is not loopback
	s.handleAI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /v1/ai = %d, want 200", rec.Code)
	}
	var st AIState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET /v1/ai body: %v", err)
	}
	return st
}

func TestAIStateListsPresets(t *testing.T) {
	s := newTestServer(t)
	st := getAI(t, s)
	if len(st.Presets) != len(rewrite.Presets) {
		t.Fatalf("presets = %d, want %d (the UI builds its buttons from these)", len(st.Presets), len(rewrite.Presets))
	}
	if st.Provider != "" || st.Reachable {
		t.Errorf("unconfigured server reports provider %q reachable %v", st.Provider, st.Reachable)
	}
	if !st.Writable {
		t.Error("a request from 127.0.0.1 must be writable")
	}
}

// The whole feature, end to end: point the server at a backend, see it answer,
// see the models list come back.
func TestAISetPointsAtABackend(t *testing.T) {
	backend := fakeBackend(t)
	s := newTestServer(t)

	body := `{"provider":"llamacpp","url":"` + backend.URL + `","model":"qwen2.5-1.5b-instruct"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:41234"
	s.handleAI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST /v1/ai = %d: %s", rec.Code, rec.Body.String())
	}

	st := getAI(t, s)
	if st.Provider != "llamacpp" || st.Model != "qwen2.5-1.5b-instruct" {
		t.Fatalf("state = %+v", st)
	}
	if st.Protocol != rewrite.ProtoOpenAI {
		t.Errorf("protocol = %q, want openai", st.Protocol)
	}
	if !st.Reachable {
		t.Error("backend serves /v1/models but is reported unreachable")
	}
	if len(st.Models) != 1 || st.Models[0] != "qwen2.5-1.5b-instruct" {
		t.Errorf("models = %v, want the one the backend reported", st.Models)
	}

	// The rewrite path must now go through that backend.
	rw, provider, model := s.rewriteClient()
	if rw == nil || provider != "llamacpp" || model != "qwen2.5-1.5b-instruct" {
		t.Fatalf("rewrite client = %v/%s/%s", rw, provider, model)
	}
	got, err := rw.Rewrite(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "One long sentence.", "", "")
	if err != nil || len(got) != 1 || got[0] != "Shorter version." {
		t.Fatalf("Rewrite via the configured backend = %v, %v", got, err)
	}
}

// The security boundary: this server listens on 0.0.0.0 so a phone can check
// its writing. Settings must not be writable from there.
func TestAISetRefusesNonLoopback(t *testing.T) {
	s := newTestServer(t)
	body := `{"provider":"openai","url":"http://evil.example/v1","model":"x"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(body))
	req.RemoteAddr = "192.168.29.77:5555"
	s.handleAI(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST from the LAN = %d, want 403", rec.Code)
	}
	rw, provider, model := s.rewriteClient()
	if rw != nil || provider != "" || model != "" {
		t.Fatalf("a LAN request changed the backend to %v/%s/%s", rw, provider, model)
	}
	// And nothing was written to disk for the next restart to pick up.
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "grammar-server", "ai.json")); err == nil {
		t.Error("a refused request still wrote the saved setting")
	}
}

func TestAISetRejectsUnknownProvider(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(`{"provider":"gpt-magic"}`))
	req.RemoteAddr = "127.0.0.1:1"
	s.handleAI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ollama") {
		t.Errorf("the error should list the known providers, got %s", rec.Body.String())
	}
}

// A provider-only body must fill in the default URL from the preset, and the
// refusal when nothing is listening must say which URL it tried — otherwise the
// UI's simplest action ("just pick LM Studio") fails with nothing to go on.
func TestAISetFillsDefaultsFromThePreset(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(`{"provider":"lmstudio"}`))
	req.RemoteAddr = "127.0.0.1:1"
	s.handleAI(rec, req)

	// Nothing answers on LM Studio's default port here, so this is a 400 — and
	// the body has to carry the URL that was probed, which is only the preset's
	// default if the preset supplied it.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 with nothing listening: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "127.0.0.1:1234") {
		t.Errorf("the error must name the URL it tried, got %s", rec.Body.String())
	}
	saved, ok := LoadSavedAI()
	if ok && saved.Provider == "lmstudio" {
		t.Error("a backend that could not be configured was still saved")
	}
}

// One model loaded, no model named: use it. Asking someone to type out the only
// model their server has is busywork, and it is the common case for llama.cpp
// and LM Studio, which serve one model at a time.
func TestAISetAdoptsTheOnlyModelTheBackendOffers(t *testing.T) {
	backend := fakeBackend(t) // reports exactly one model
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai",
		strings.NewReader(`{"provider":"llamacpp","url":"`+backend.URL+`"}`))
	req.RemoteAddr = "127.0.0.1:1"
	s.handleAI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var st AIState
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Model != "qwen2.5-1.5b-instruct" {
		t.Errorf("model = %q, want the only one the backend offers", st.Model)
	}
}

// Several models offered: refuse and name them, rather than picking one at
// random for the user.
func TestAISetRefusesToGuessAmongManyModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"},{"id":"c"}]}`))
	}))
	defer srv.Close()
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(`{"provider":"openai","url":"`+srv.URL+`"}`))
	req.RemoteAddr = "127.0.0.1:1"
	s.handleAI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "a") || !strings.Contains(rec.Body.String(), "c") {
		t.Errorf("the error should list the models, got %s", rec.Body.String())
	}
}

func TestAISetSavesAndNoneDisables(t *testing.T) {
	backend := fakeBackend(t)
	s := newTestServer(t)

	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1"
		s.handleAI(rec, req)
		if rec.Code != 200 {
			t.Fatalf("POST %s = %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	post(`{"provider":"llamacpp","url":"` + backend.URL + `","model":"m"}`)

	saved, ok := LoadSavedAI()
	if !ok {
		t.Fatal("the choice was not saved: it will not survive a restart")
	}
	if saved.Provider != "llamacpp" || saved.URL != backend.URL || saved.Model != "m" {
		t.Fatalf("saved = %+v", saved)
	}

	post(`{"provider":"none"}`)
	rw, provider, _ := s.rewriteClient()
	if rw != nil {
		t.Error("provider none left a live client")
	}
	if provider != rewrite.ProviderNone {
		t.Errorf("provider = %q, want %q so the UI can show it as off", provider, rewrite.ProviderNone)
	}
	saved, ok = LoadSavedAI()
	if !ok || saved.Provider != rewrite.ProviderNone {
		t.Errorf("saved after none = %+v (ok=%v)", saved, ok)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:41234": true,
		"[::1]:41234":     true,
		"192.168.29.77:5": false,
		"10.0.0.1:5":      false,
		"":                false,
	}
	for addr, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = addr
		if got := isLoopback(req); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
