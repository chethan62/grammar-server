// Package rewrite asks a local model to rephrase a sentence.
//
// It is the only part of grammar-server that needs a model: it is never on the
// /v2/check path, every endpoint behaves identically with the backend stopped,
// and the server never downloads a model behind your back.
//
// Two protocols cover every backend worth naming:
//
//	ollama — Ollama's native /api/generate, which alone can carry keep_alive
//	openai — the /v1/chat/completions shape that llama.cpp server, LM Studio,
//	         vLLM, OpenRouter, Ollama's own /v1 shim and everything else speak
//
// So "which backend" is a preset, and a preset is a protocol plus a default URL.
package rewrite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sentinels the API layer turns into user-facing errors: they name different
// fixes (start the backend, pull or name the model, pick a smaller one, set the
// key), so they stay distinct rather than collapsing into one message.
var (
	ErrUnavailable  = errors.New("rewrite backend unavailable")
	ErrModelMissing = errors.New("rewrite model not installed")
	ErrTimeout      = errors.New("rewrite timed out")
	ErrAuth         = errors.New("rewrite backend rejected the API key")
)

// Rewriter is one backend, ready to answer. One method on purpose: the server
// keeps provider/url/model as its own state so it can report them to a UI, and
// a two-method interface would only duplicate that.
type Rewriter interface {
	Rewrite(ctx context.Context, text, tone, intent string) ([]string, error)
}

// Streamer is a Rewriter that can hand its answer over while it is being written.
//
// Deliberately a second, optional interface rather than a fourth method on Rewriter: only a backend
// that speaks a streaming protocol can do this, and a caller should be able to ask with a type
// assertion and fall back to the one-body path when the answer is no. Ollama is the one that can
// today — measured on this machine, its first words arrive 0.05s after the request against 2.2s for
// the finished sentence, which is the difference between a card that looks stuck and one that types.
type Streamer interface {
	RewriteStream(ctx context.Context, text, tone, intent string, onDelta func(string)) ([]string, error)
}

// The two protocols, plus the id that means "no rewriting at all".
const (
	ProtoOllama = "ollama"
	ProtoOpenAI = "openai"

	// ProviderNone turns rewriting off without a restart. It is not a preset
	// (there is nothing to configure) but the UI offers it as a choice, and
	// every other endpoint behaves identically while it is selected.
	ProviderNone = "none"
)

// KeyEnvFor names the environment variable a backend reads its API key from, so
// an error message can tell someone which one to set. Local backends ask for
// nothing and get an empty name.
func KeyEnvFor(provider string) string {
	if p, ok := Resolve(provider); ok {
		return p.KeyEnv
	}
	return ""
}

// KeyFilePath is where a key entered through the settings API is kept, and the write path too:
// one owner for "where a key comes from", so no caller invents a second location.
//
// It is a separate file from ai.json, mode 0600, because a secret does not belong in a config file
// that a settings UI reads back. Nothing stores it in the JSON, nothing returns it over HTTP, and
// the only way to set it is a loopback request.
func KeyFilePath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grammar-server", "ai.key")
}

// SaveKey stores a key for the current backend: the desktop keyring when there is one, and that file
// otherwise. 0600 from the first byte written, so the file is never briefly readable by anyone else.
//
// A keyring that refuses — no secret-tool, no session bus, a locked wallet — must never lose the
// key, so the file is the fallback rather than the loser. When the keyring does take it, the file is
// removed: a plaintext copy of a secret that is already in the keyring is the exact thing this
// avoids, and a removal that fails does not fail the save — the keyring is what is read first, so
// the leftover is a privacy wart rather than a broken key.
func SaveKey(key string) error {
	key = strings.TrimSpace(key)
	if key != "" && keyringPut(key) {
		if path := KeyFilePath(); path != "" {
			_ = os.Remove(path) // best effort; see above
		}
		return nil
	}
	path := KeyFilePath()
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	// WriteFile does not tighten the mode of a file that already exists, and a wholesale edit or a
	// restored backup can leave one world-readable. A secret's permissions are not a suggestion.
	return os.Chmod(path, 0o600)
}

// keyFor is the whole answer to "which key is in force, and where did it come from": the environment
// first, then the desktop keyring, then the file. The environment still wins — a key set that way
// before the settings API existed keeps working, and keeps meaning what it meant.
func keyFor(provider string) (string, string) {
	env := KeyEnvFor(provider)
	if env == "" {
		return "", "" // this backend takes no key at all
	}
	if value := strings.TrimSpace(getenv(env)); value != "" {
		return value, "env"
	}
	if key, _ := keyringGet(); key != "" {
		return key, "keyring"
	}
	path := KeyFilePath()
	if path == "" {
		return "", ""
	}
	if b, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(b)); value != "" {
			return value, "file"
		}
	}
	return "", ""
}

// APIKeyFor is the key itself. Kept as the entry point callers already use.
func APIKeyFor(provider string) string {
	value, _ := keyFor(provider)
	return value
}

// KeySourceFor names where that key came from — "env", "keyring", "file", or "" for none — so a
// settings panel can say where a credential actually lives instead of guessing.
func KeySourceFor(provider string) string {
	_, source := keyFor(provider)
	return source
}

// ---- the desktop keyring ------------------------------------------------------------------------

// A key typed into the settings panel belongs in the keyring: encrypted at rest, unlocked with the
// login session, and not a plaintext file that follows a config directory into backups and dotfile
// repositories. secret-tool is libsecret's CLI and is present on both GNOME and KDE desktops.
//
// Lookup is cached because it is a process spawn, and a rephrase should not pay for one. A save
// seeds the cache, since whoever just typed a key is about to press Test.
var (
	// A package variable so tests (and CI, which has no keyring at all) never exec a real one.
	keyringExec = func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "secret-tool", args...)
	}
	keyringTimeout = 2 * time.Second
	keyringCached  struct {
		sync.Mutex
		read bool
		key  string
	}
)

// keyringOff is the escape hatch, and it exists because of a real accident: this package's tests save
// keys through the API, and once the keyring became the preferred home, a unit test wrote a real item
// into the developer's own wallet — while CI, which has no keyring, never noticed anything wrong. A
// test suite that must not touch the session keyring sets GRAMMAR_NO_KEYRING, and so can an install.
func keyringOff() bool { return os.Getenv("GRAMMAR_NO_KEYRING") != "" }

// keyringArgs is the item's identity, and it has to be identical for store and lookup or a key is
// written somewhere nothing reads. One key for the current backend, mirroring the file it replaces.
func keyringArgs(action string, extra ...string) []string {
	return append(append([]string{action}, extra...), "service", "grammar-server", "kind", "api-key")
}

// keyringGet returns the stored key and whether the keyring answered at all. A missing secret-tool is
// a different thing from "nothing stored": it is what tells SaveKey to keep using the file.
func keyringGet() (string, bool) {
	if keyringOff() {
		return "", false
	}
	keyringCached.Lock()
	if keyringCached.read {
		key := keyringCached.key
		keyringCached.Unlock()
		return key, true
	}
	keyringCached.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	out, err := keyringExec(ctx, keyringArgs("lookup")...).Output()
	if err != nil {
		return "", false
	}
	key := strings.TrimSpace(string(out))
	keyringCached.Lock()
	keyringCached.read, keyringCached.key = true, key
	keyringCached.Unlock()
	return key, true
}

// keyringPut stores the key and reports whether the keyring took it.
func keyringPut(key string) bool {
	if keyringOff() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	cmd := keyringExec(ctx, keyringArgs("store", "--label", "grammar-server: AI key")...)
	cmd.Stdin = strings.NewReader(strings.TrimSpace(key) + "\n")
	if err := cmd.Run(); err != nil {
		return false
	}
	keyringCached.Lock()
	keyringCached.read, keyringCached.key = true, strings.TrimSpace(key)
	keyringCached.Unlock()
	return true
}

// getenv is a variable so tests can supply a key without touching the process
// environment.
var getenv = func(k string) string { return os.Getenv(k) }

// Presets are the backends offered by name. Anything OpenAI-compatible that is
// not listed still works: provider "openai" with your own URL and model.
type Preset struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Protocol string `json:"protocol"`
	URL      string `json:"url"`    // default base URL, no trailing slash
	Model    string `json:"model"`  // a suggestion, not a requirement
	Hint     string `json:"hint"`   // shown in the UI under the model field
	KeyEnv   string `json:"keyEnv"` // env var holding an API key, when one is needed
	Local    bool   `json:"local"`  // false = the text leaves this machine
}

// Presets is ordered as the UI shows them: local ones first, cheapest first.
var Presets = []Preset{
	{
		ID: "ollama", Label: "Ollama", Protocol: ProtoOllama,
		URL: "http://127.0.0.1:11434", Model: "qwen2.5:1.5b", Local: true,
		Hint: "Measured here: 0.9s warm, 6.3s cold per rephrase. Ollama is the only backend that can hold the model in memory between clicks.",
	},
	{
		ID: "llamacpp", Label: "llama.cpp server", Protocol: ProtoOpenAI,
		URL: "http://127.0.0.1:8080", Model: "", Local: true,
		Hint: "Start it with: llama-server -m model.gguf --port 8080. The model name is whatever it reports.",
	},
	{
		ID: "lmstudio", Label: "LM Studio", Protocol: ProtoOpenAI,
		URL: "http://127.0.0.1:1234", Model: "", Local: true,
		Hint: "Enable the local server in LM Studio's Developer tab, then press the model field to list what is loaded.",
	},
	{
		ID: "vllm", Label: "vLLM", Protocol: ProtoOpenAI,
		URL: "http://127.0.0.1:8000", Model: "", Local: true,
		Hint: "python -m vllm.entrypoints.openai.api_server --model <model>. Built for throughput, not for one sentence at a time.",
	},
	{
		ID: "openrouter", Label: "OpenRouter", Protocol: ProtoOpenAI,
		URL: "https://openrouter.ai/api/v1", Model: "", Local: false,
		Hint:   "Cloud: the text you rewrite leaves this machine. Put the key in OPENROUTER_API_KEY and the value never reaches this config.",
		KeyEnv: "OPENROUTER_API_KEY",
	},
	{
		ID: "openai", Label: "OpenAI-compatible (any other)", Protocol: ProtoOpenAI,
		URL: "", Model: "", Local: false,
		Hint:   "Any server speaking /v1/chat/completions — Groq, Together, llama-box, an old vLLM behind nginx. Give the full base URL.",
		KeyEnv: "OPENAI_API_KEY",
	},
}

// Resolve returns the preset with this id. The returned bool is false for an id
// we do not know, which the API layer reports rather than guessing a protocol.
func Resolve(id string) (Preset, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Build returns the backend for a provider id. An unknown provider or an empty
// URL or model yields nil: a misconfigured rewrite must leave every other
// endpoint untouched, which is what a nil Rewriter means everywhere else in this
// server. (The Ollama client also has its own New, kept because it is the shape
// the existing callers and tests use.)
func Build(provider, baseURL, model, apiKey string) Rewriter {
	p, ok := Resolve(provider)
	if !ok || strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
		return nil
	}
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if p.Protocol == ProtoOllama {
		return &Client{URL: baseURL, Model: model}
	}
	return &OpenAI{URL: baseURL, Model: model, APIKey: apiKey}
}

// IDForURL guesses which preset a URL belongs to, so the UI can light up the
// right button when someone pastes a URL instead of picking a name. It matches
// on port and host only; anything unrecognised is "openai" (the shape that
// works with the most servers) rather than a wrong guess with a real name.
func IDForURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "openai"
	}
	port := u.Port()
	host := strings.ToLower(u.Hostname())
	local := host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "0.0.0.0"
	for _, p := range Presets {
		pu, err := url.Parse(p.URL)
		if err != nil {
			continue
		}
		// Same host:port means the same server, regardless of any /v1 suffix.
		if pu.Port() == port && strings.ToLower(pu.Hostname()) == host && local {
			return p.ID
		}
	}
	switch port {
	case "11434":
		return "ollama"
	case "8080":
		return "llamacpp"
	case "1234":
		return "lmstudio"
	case "8000":
		return "vllm"
	}
	if strings.Contains(host, "openrouter.ai") {
		return "openrouter"
	}
	return "openai"
}

// DefaultTimeout bounds one rewrite. A warm qwen2.5:1.5b answers in ~2s on CPU
// (measured: 50 generated tokens at 16 tok/s); 20s covers a cold model load
// without holding a connection for a minute. The HTTP server's own write timeout
// is 30s.
const DefaultTimeout = 20 * time.Second

// Alternatives is how many rephrasings to ask for, and tokenBudget the room that
// allows them. Two is deliberate: measured on this CPU, one alternative takes
// ~1s and three take ~3.2s, and the third is usually the weakest or a verbatim
// echo of the input (which gets filtered out below).
const (
	Alternatives = 2
	tokenBudget  = 70
	temperature  = 0.3 // a rewrite should not be creative
)

// instruction is the prompt both clients send, so they cannot drift apart: one
// client puts it in a system message, the other in front of the text.
//
// It stays one sentence. Prefill is the second cost after model load (67 prompt
// tokens here, 0.09s) and a small model uses none of a long instruction.
//
// Measured on qwen2.5:1.5b, same sentence either way:
//
//	no hints    "Because it was late, we used the car to drive home."   (good)
//	tone=formal "Because of the late hour, we employed the car for the
//	             purpose of returning home."                           (padded)
//
// Asking a 1.5B model for a tone makes it add words rather than fix them, and
// adding "do not add words" to the prompt did not change that. Callers should
// leave both hints empty for the best rewrite; intent=concise is the one worth
// offering, because it is the one this size of model can actually do.
func instruction(tone, intent string) string {
	var b strings.Builder
	b.WriteString("Rephrase this sentence")
	if intent != "" {
		fmt.Fprintf(&b, " to be more %s", intent)
	}
	if tone != "" {
		fmt.Fprintf(&b, " in a %s tone", tone)
	}
	fmt.Fprintf(&b, ". Keep the meaning. Reply with %d alternatives, one per line, no numbering, no commentary.", Alternatives)
	return b.String()
}

// candidates splits a model's reply into usable alternatives: numbering and
// bullets stripped, blanks dropped, an echo of the original dropped (a small
// model likes to return the input unchanged, which is not a suggestion).
func candidates(reply, original string) []string {
	orig := strings.ToLower(strings.TrimSpace(original))
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•)0123456789. "))
		if line == "" {
			continue
		}
		key := strings.ToLower(line)
		if key == orig || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
		if len(out) == Alternatives {
			break
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
