// Package config holds the grammar-server configuration, read from a YAML file
// and overridden by CLI flags/environment variables.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the full grammar-server configuration.
type Config struct {
	Port    int    `yaml:"port"    json:"port"`
	Dialect string `yaml:"dialect" json:"dialect"`
	Harper  string `yaml:"harper"  json:"harper"`
	LogFmt  string `yaml:"log_fmt"  json:"log_fmt"` // "text" or "json"

	// Local rewriting. An empty rewrite_model disables POST /v2/rewrite (it then
	// answers 503 with instructions); no other endpoint needs a model, and the
	// server behaves exactly the same with Ollama stopped.
	OllamaURL    string `yaml:"ollama_url"    json:"ollama_url"`
	RewriteModel string `yaml:"rewrite_model" json:"rewrite_model"`
}

// Defaults returns a Config with sensible defaults.
//
// rewrite_model is qwen2.5:1.5b on purpose. Measured on this CPU (8 threads, the
// GPU is capped and unused): 0.9s warm / 6.3s cold per rephrase, against 2.8s
// warm / 20s cold for qwen3.5:4b. The bigger model reads better and keeps you
// waiting three times as long; that is a poor trade for a button in a text box.
func Defaults() Config {
	return Config{
		Port:    8875,
		Dialect: "American",
		Harper:  "harper-ls",
		LogFmt:  "text",

		OllamaURL:    "http://127.0.0.1:11434",
		RewriteModel: "qwen2.5:1.5b",
	}
}

// Load reads a YAML config file at the given path. If the file does not exist,
// defaults are returned. CLI flags should be applied afterward to override.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks the config values and returns an error if any are invalid.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	}
	validDialects := map[string]bool{
		"American": true, "British": true, "Canadian": true, "Australian": true, "Indian": true,
	}
	if !validDialects[c.Dialect] {
		return fmt.Errorf("unknown dialect %q (valid: American, British, Canadian, Australian, Indian)", c.Dialect)
	}
	if c.LogFmt != "text" && c.LogFmt != "json" {
		return fmt.Errorf("log_fmt must be 'text' or 'json', got %q", c.LogFmt)
	}
	return nil
}
