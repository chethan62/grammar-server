// Package config holds the grammar-server configuration, read from a YAML file
// and overridden by CLI flags/environment variables.
package config

import (
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
