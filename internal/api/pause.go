package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The pause state: whether the checker is paused and for how long, plus the
// applications it ignores entirely. These are owned by the watcher (grammar-watch)
// and the grammar-pause CLI, not by the engine — the engine only knows about
// ignored words. This endpoint surfaces the watcher's state so the UI can show
// the current pause status without owning it.

func pausePausedPath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "grammar-server", "paused-until")
}

func pauseBlockedPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grammar-server", "blocked-apps")
}

func readPaused(path string) (bool, string) {
	if path == "" {
		return false, ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	// A Unix timestamp in seconds, which is what grammar_core.write_pause writes — `"%d\n" % until`, an
	// integer, not a formatted date. Parsed as a float to mirror the reader on the other side
	// (`float(fh.read().strip())`), so both ends accept the same bytes.
	//
	// This was time.Parse(time.RFC3339, …) first, which was wrong, and worse: the test asserted the same
	// wrong format, so it passed while every real parse failed and the endpoint reported "running" through
	// an actual pause. A test written from the assumption pins the assumption, not the interface — this
	// one only surfaced by pausing for real and asking the endpoint.
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return false, ""
	}
	until := time.Unix(int64(seconds), 0)
	if time.Now().Before(until) {
		return true, until.UTC().Format(time.RFC3339)
	}
	return false, ""
}

func listBlocked(path string) []string {
	if path == "" {
		return []string{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	// A set, because that is what the watcher does with it — it asks whether an app is blocked, so a line
	// written twice means nothing to it. The window prints this list to a person, where a repeat does mean
	// something: "kate, kate, signal" reads as an engine that cannot dedupe its own state.
	seen := make(map[string]bool)
	out := []string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	// Read-only: the pause state is owned by grammar-watch/grammar-pause, not
	// the engine. The UI only needs to display it.
	paused, until := readPaused(pausePausedPath())
	blocked := listBlocked(pauseBlockedPath())

	writeJSON(w, 200, map[string]any{
		"paused":  paused,
		"until":   until,
		"blocked": blocked,
	})
}
