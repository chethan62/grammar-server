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
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	return Config{
		Port:    8875,
		Dialect: "American",
		Harper:  "harper-ls",
		LogFmt:  "text",
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
