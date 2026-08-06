// Package binaries embeds copies of harper-ls and harper-cli so the grammar-server
// can be distributed as a single self-contained binary with no external dependencies.
//
// Build requirement: the embed target files must exist at compile time. Copy them
// from the system before building:
//
//	cp /usr/bin/harper-ls /usr/bin/harper-cli internal/binaries/
//	go build -o bin/grammar-server ./cmd/server
package binaries

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// FS holds the embedded harper binaries.
//
//go:embed harper-ls harper-cli
var FS embed.FS

// Extract writes the embedded harper-* binaries to dir and returns their paths.
// On subsequent calls the files are not re-extracted.
func Extract(dir string) (ls, cli string, err error) {
	if e := os.MkdirAll(dir, 0o755); e != nil {
		return "", "", fmt.Errorf("embed: mkdir: %w", e)
	}
	files := []string{"harper-ls", "harper-cli"}
	for _, name := range files {
		dst := filepath.Join(dir, name)
		if _, statErr := os.Stat(dst); statErr == nil {
			continue // already extracted
		}
		data, readErr := FS.ReadFile(name)
		if readErr != nil {
			return "", "", fmt.Errorf("embed: read %s: %w", name, readErr)
		}
		if writeErr := os.WriteFile(dst, data, 0o755); writeErr != nil {
			return "", "", fmt.Errorf("embed: write %s: %w", name, writeErr)
		}
	}
	return filepath.Join(dir, "harper-ls"), filepath.Join(dir, "harper-cli"), nil
}
