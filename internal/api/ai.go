package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"grammar-server/internal/rewrite"
)

// The rewrite backend is the one setting worth changing without a restart: it
// names a program running somewhere else on this machine, and which one is
// running is not something a config file should have the last word on.
//
// GET  /v1/ai   what is configured, whether it answers, and what models it has
// POST /v1/ai   change it (this machine only)
//
// Writes are refused unless the request came from loopback. The server binds
// 0.0.0.0 on purpose so a phone on the LAN can check its writing; that must not
// also mean anyone on the LAN can point this server at a URL of their choosing
// and make it fetch it.

// AISetting is the body of POST /v1/ai. An empty url or model falls back to the
// provider's default, so a client only has to name the product.
type AISetting struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Model    string `json:"model"`
}

// AIState is the answer to GET /v1/ai: everything a settings panel needs, and
// nothing secret. keySet is a boolean because the value never leaves the
// server's environment.
type AIState struct {
	Provider  string           `json:"provider"`
	URL       string           `json:"url"`
	Model     string           `json:"model"`
	Protocol  string           `json:"protocol"`
	Local     bool             `json:"local"`
	Hint      string           `json:"hint"`
	KeyEnv    string           `json:"keyEnv,omitempty"`
	KeySet    bool             `json:"keySet"`
	Reachable bool             `json:"reachable"`
	Models    []string         `json:"models"`
	Presets   []rewrite.Preset `json:"presets"`
	Writable  bool             `json:"writable"` // false when this request is not from this machine
}

// aiProbeTimeout bounds the reachability probe. A settings panel must render
// while the backend is down: an unreachable server is the normal state of a
// backend nobody has started yet.
const aiProbeTimeout = 2500 * time.Millisecond

func (s *Server) handleAI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, s.aiState(r))
	case http.MethodPost:
		s.setAI(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) aiState(r *http.Request) AIState {
	s.rwMu.Lock()
	provider, url, model, rw := s.rwProvider, s.rwURL, s.rwModel, s.rw
	s.rwMu.Unlock()

	st := AIState{
		Provider: provider,
		URL:      url,
		Model:    model,
		Presets:  rewrite.Presets,
		Writable: isLoopback(r),
	}
	if p, ok := rewrite.Resolve(provider); ok {
		st.Protocol, st.Local, st.Hint, st.KeyEnv = p.Protocol, p.Local, p.Hint, p.KeyEnv
		st.KeySet = p.KeyEnv != "" && rewrite.APIKeyFor(provider) != ""
	}
	if rw == nil {
		return st
	}

	// The probe asks for the model list rather than pinging /status: the same
	// round trip then answers both "is it up" and "what can I choose".
	ctx, cancel := context.WithTimeout(r.Context(), aiProbeTimeout)
	defer cancel()
	models, err := rewrite.ListModels(ctx, url, rewrite.APIKeyFor(provider))
	st.Reachable = err == nil
	if st.Reachable {
		st.Models = models
	} else {
		st.Models = []string{}
	}
	return st
}

// setAI applies a new backend. It validates before it writes, and it saves to
// the user's config directory so the choice survives a restart — a setting that
// silently reverts is worse than no setting.
func (s *Server) setAI(w http.ResponseWriter, r *http.Request) {
	// Refuse before reading the body: an unauthorised caller should not even get
	// to make the server parse its input.
	if !isLoopback(r) {
		writeError(w, http.StatusForbidden,
			"changing the rewrite backend is allowed from this machine only (this server accepts checks from anywhere, but not settings)")
		return
	}

	var in AISetting
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))

	if in.Provider == rewrite.ProviderNone || in.Provider == "" {
		s.SetRewrite(rewrite.ProviderNone, "", "")
		if err := saveAI(AISetting{Provider: rewrite.ProviderNone}); err != nil {
			writeError(w, http.StatusInternalServerError, "rewrite disabled, but saving the choice failed: %v", err)
			return
		}
		writeJSON(w, 200, s.aiState(r))
		return
	}

	preset, ok := rewrite.Resolve(in.Provider)
	if !ok {
		names := make([]string, 0, len(rewrite.Presets))
		for _, p := range rewrite.Presets {
			names = append(names, p.ID)
		}
		writeError(w, http.StatusBadRequest, "unknown provider %q (known: %s, %s)",
			in.Provider, strings.Join(names, ", "), rewrite.ProviderNone)
		return
	}
	if strings.TrimSpace(in.URL) == "" {
		in.URL = preset.URL
	}
	if strings.TrimSpace(in.Model) == "" {
		in.Model = preset.Model
	}
	if in.URL == "" {
		writeError(w, http.StatusBadRequest, "provider %q needs a url", in.Provider)
		return
	}
	if in.Model == "" {
		// A local server usually has exactly one model loaded, and asking someone
		// to type it out is busywork. With more than one there is a real choice to
		// make, and the UI lists them, so we stop and say so.
		ctx, cancel := context.WithTimeout(r.Context(), aiProbeTimeout)
		models, err := rewrite.ListModels(ctx, in.URL, rewrite.APIKeyFor(in.Provider))
		cancel()
		switch {
		case err == nil && len(models) == 1:
			in.Model = models[0]
		case err == nil && len(models) > 1:
			writeError(w, http.StatusBadRequest,
				"provider %q offers %d models — name the one to use (one of: %s)",
				in.Provider, len(models), strings.Join(models, ", "))
			return
		default:
			writeError(w, http.StatusBadRequest,
				"provider %q needs a model name, and nothing answered at %s to ask (for a local server: start it first)", in.Provider, in.URL)
			return
		}
	}
	if preset.KeyEnv != "" && rewrite.APIKeyFor(in.Provider) == "" {
		// Not fatal: some proxies want no key. Worth saying out loud anyway,
		// because the failure it causes (401 at rewrite time) is far from here.
		writeJSON(w, 200, s.applyAI(r, in, "no "+preset.KeyEnv+" in the server's environment — rewriting will answer 503 until you set it and restart"))
		return
	}

	writeJSON(w, 200, s.applyAI(r, in, ""))
}

func (s *Server) applyAI(r *http.Request, in AISetting, warning string) AIState {
	s.SetRewrite(in.Provider, in.URL, in.Model)
	_ = saveAI(in) // a failed save must not fail the change: rewriting works now, it just will not survive a restart
	st := s.aiState(r)
	if warning != "" {
		st.Hint = warning + ". " + st.Hint
	}
	return st
}

// isLoopback reports whether a request came from this machine.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// aiPath is where the choice is kept: the user's config directory, never the
// repo or the server's working directory.
func aiPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grammar-server", "ai.json")
}

func saveAI(in AISetting) error {
	path := aiPath()
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// LoadSavedAI reads the backend chosen through the API, if one was. main calls
// it at startup so that choice outranks the config file: the config file sets
// the default, the API changes the current one.
func LoadSavedAI() (AISetting, bool) {
	path := aiPath()
	if path == "" {
		return AISetting{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return AISetting{}, false
	}
	var in AISetting
	if err := json.Unmarshal(b, &in); err != nil {
		return AISetting{}, false
	}
	return in, true
}
