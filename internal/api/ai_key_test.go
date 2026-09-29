package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grammar-server/internal/rewrite"
)

// A key entered through the settings API: accepted, stored privately, used by the server — and
// never handed back. The last part is the whole reason this feature could exist at all: the
// project's promise was that a key never leaves the machine it was typed on, and a settings panel
// that echoes a secret back over HTTP would break it for anyone who can reach the port.
func TestAISavesAPIKeyWithoutEverReturningIt(t *testing.T) {
	backend := fakeBackend(t)
	s := newTestServer(t)

	const secret = "sk-test-MUST-NOT-BE-RETURNED-abcdef123456"
	body := `{"provider":"openai","url":"` + backend.URL + `","model":"qwen2.5-1.5b-instruct","apiKey":"` + secret + `"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:41234"
	s.handleAI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST /v1/ai = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("the response body contains the API key")
	}
	var st AIState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("POST body: %v", err)
	}
	if !st.KeySet {
		t.Error("keySet is false after a key was saved — a panel would keep asking for one")
	}
	if st.KeyEnv != "OPENAI_API_KEY" {
		t.Errorf("keyEnv = %q, want OPENAI_API_KEY so the UI can name the variable", st.KeyEnv)
	}

	// ai.json is a file the settings UI reads back, so it must hold no secret either.
	saved, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "grammar-server", "ai.json"))
	if err != nil {
		t.Fatalf("reading ai.json: %v", err)
	}
	if strings.Contains(string(saved), secret) {
		t.Error("ai.json contains the API key")
	}
	if !strings.Contains(string(saved), "qwen2.5-1.5b-instruct") {
		t.Errorf("ai.json lost the model: %s", saved)
	}

	// The key file: it exists, it is private, and it holds the key.
	info, err := os.Stat(rewrite.KeyFilePath())
	if err != nil {
		t.Fatalf("no key file at %s: %v", rewrite.KeyFilePath(), err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600 — a secret another user can read is not stored", perm)
	}
	if got := rewrite.APIKeyFor("openai"); got != secret {
		t.Errorf("APIKeyFor = %q, want the saved key (the backend would 401 without it)", got)
	}

	// An environment variable still outranks the file: that is how a key was set before this
	// existed, and a feature that silently changed its meaning would be a regression.
	t.Setenv("OPENAI_API_KEY", "env-key-wins")
	if got := rewrite.APIKeyFor("openai"); got != "env-key-wins" {
		t.Errorf("APIKeyFor = %q with OPENAI_API_KEY set, want the environment to win", got)
	}
}

// Saving a key in the same request that sets the backend must not also warn that no key is set —
// the response said keySet true and "no OPENAI_API_KEY" in the same breath, because the warning was
// computed before the key was written.
func TestAIKeyInTheSameRequestSuppressesTheMissingKeyWarning(t *testing.T) {
	s := newTestServer(t)
	body := `{"provider":"openai","url":"http://127.0.0.1:9","model":"m","apiKey":"a-real-looking-key"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:41234"
	s.handleAI(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST /v1/ai = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var st AIState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("POST body: %v", err)
	}
	if !st.KeySet {
		t.Error("keySet is false after a key was sent")
	}
	if strings.Contains(st.Hint, "no OPENAI_API_KEY") {
		t.Errorf("hint contradicts keySet: %q", st.Hint)
	}
}

// An empty apiKey means "leave it alone", which is what a password box that cannot show the saved
// value must send. Without this, saving the form would wipe a key nobody touched.
func TestAIEmptyKeyLeavesTheStoredOneAlone(t *testing.T) {
	backend := fakeBackend(t)
	s := newTestServer(t)
	if err := rewrite.SaveKey("the-original-key"); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}

	post := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/ai", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:41234"
		s.handleAI(rec, req)
		if rec.Code != 200 {
			t.Fatalf("POST /v1/ai = %d (%s), want 200", rec.Code, rec.Body.String())
		}
	}
	post(`{"provider":"openai","url":"` + backend.URL + `","model":"m","apiKey":""}`)
	if got := rewrite.APIKeyFor("openai"); got != "the-original-key" {
		t.Errorf("APIKeyFor = %q after saving with an empty key, want the stored key kept", got)
	}
	post(`{"provider":"openai","url":"` + backend.URL + `","model":"m","apiKey":"replacement-key"}`)
	if got := rewrite.APIKeyFor("openai"); got != "replacement-key" {
		t.Errorf("APIKeyFor = %q after saving a new key, want it replaced", got)
	}
}
