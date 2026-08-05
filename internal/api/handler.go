// Package api implements the LanguageTool-compatible HTTP endpoints.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"grammar-server/internal/engine"
)

// CheckRequest mirrors the LanguageTool /v2/check request body.
type CheckRequest struct {
	Text          string   `json:"text"`
	Language      string   `json:"language"`
	EnabledRules  []string `json:"enabledRules"`  // if non-empty, return only these
	DisabledRules []string `json:"disabledRules"` // never report these
	MotherTongue  string   `json:"motherTongue"`
}

// CheckResponse mirrors the LanguageTool /v2/check response.
type CheckResponse struct {
	Software       Software  `json:"software"`
	Language       LangInfo  `json:"language"`
	Matches        []Match   `json:"matches"`
	SentenceRanges [][]int64 `json:"sentenceRanges"`
}

type Software struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	BuildDate  string `json:"buildDate"`
	APIVersion int    `json:"apiVersion"`
	Status     string `json:"status"`
	Premium    bool   `json:"premium"`
}

type LangInfo struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

type Match struct {
	Message      string        `json:"message"`
	ShortMessage string        `json:"shortMessage,omitempty"`
	Replacements []Replacement `json:"replacements"`
	Rule         RuleInfo      `json:"rule"`
	Type         TypeInfo      `json:"type"`
	Offset       int64         `json:"offset"`
	Length       int64         `json:"length"`
	Context      MatchContext  `json:"context"`
	Sentence     string        `json:"sentence,omitempty"`
}

type Replacement struct {
	Value string `json:"value"`
}

type MatchContext struct {
	Text   string `json:"text"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type TypeInfo struct {
	TypeName string `json:"typeName"`
}

type RuleInfo struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	IssueType   string   `json:"issueType"`
	Category    Category `json:"category"`
}

type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// kategory maps a harper lint kind to an LT "issueType" (category name).
func issueTypeForKind(kind string) string {
	switch kind {
	case "spelling", "grammar":
		return "misspelling"
	case "style":
		return "style"
	case "typography":
		return "typography"
	default:
		return "style"
	}
}

func categoryForKind(kind string) Category {
	id := "STYLE"
	name := "style"
	switch kind {
	case "grammar":
		id, name = "GRAMMAR", "grammar"
	case "spelling":
		id, name = "SPELLING", "spelling"
	case "typography":
		id, name = "TYPOS", "typos"
	}
	return Category{ID: id, Name: name}
}

func dialectForLang(lang string) string {
	switch {
	case strings.Contains(lang, "GB"):
		return "British"
	case strings.Contains(lang, "CA"):
		return "Canadian"
	case strings.Contains(lang, "AU"):
		return "Australian"
	case strings.Contains(lang, "IN"):
		return "Indian"
	default:
		return "American"
	}
}

func langCode(lang string) string {
	if lang == "" {
		return "en-US"
	}
	return lang
}

// Server owns the HTTP handlers and the backing engine.
type Server struct {
	eng     *engine.Harper
	version string
}

func NewServer(eng *engine.Harper) *Server {
	return &Server{eng: eng, version: "0.1.0"}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", s.handleCheck)
	mux.HandleFunc("/v2/languages", s.handleLanguages)
	mux.HandleFunc("/", s.handleRoot)
	return logRequests(mux)
}

func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"service": "grammar-server", "status": "OK", "version": s.version})
}

func (s *Server) handleLanguages(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, []LangInfo{
		{Name: "English (US)", Code: "en-US"},
		{Name: "English (UK)", Code: "en-GB"},
		{Name: "English (Canada)", Code: "en-CA"},
		{Name: "English (Australia)", Code: "en-AU"},
		{Name: "English (India)", Code: "en-IN"},
	})
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req CheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}

	if err := s.eng.SetDialect(dialectForLang(req.Language)); err != nil {
			writeError(w, http.StatusInternalServerError, "set dialect: %v", err)
			return
		}
		lints, err := s.eng.Check(req.Text)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "engine error: %v", err)
		return
	}

	resp := s.buildResponse(req, lints)
	writeJSON(w, 200, resp)
}

func (s *Server) buildResponse(req CheckRequest, lints []engine.Lint) CheckResponse {
	enabled := map[string]bool{}
	for _, id := range req.EnabledRules {
		if id != "" {
			enabled[id] = true
		}
	}
	disabled := map[string]bool{}
	for _, id := range req.DisabledRules {
		if id != "" {
			disabled[id] = true
		}
	}
	useEnable := len(req.EnabledRules) > 0

	matches := make([]Match, 0, len(lints))
	for _, l := range lints {
		if disabled[l.Rule] {
			continue
		}
		if useEnable && !enabled[l.Rule] {
			continue
		}
		ctx := buildContext(req.Text, l.CharStart, l.CharEnd)
		reps := make([]Replacement, 0, len(l.Replacements))
		for _, rep := range l.Replacements {
			if rep != "" {
				reps = append(reps, Replacement{Value: rep})
			}
		}
		matches = append(matches, Match{
			Offset:       int64(l.CharStart),
			Length:       int64(l.CharEnd - l.CharStart),
			Message:      l.Message,
			Replacements: reps,
			Context:      ctx,
			Rule: RuleInfo{
				ID:          l.Rule,
				Description: l.Message,
				IssueType:   issueTypeForKind(l.Kind),
				Category:    categoryForKind(l.Kind),
			},
			Type: TypeInfo{TypeName: l.Kind},
		})
	}
	return CheckResponse{
		Software: Software{
			Name: "grammar-server", Version: s.version,
			BuildDate: time.Now().UTC().Format(time.RFC3339),
			APIVersion: 2, Status: "OK",
		},
		Language: LangInfo{Name: langName(req.Language), Code: langCode(req.Language)},
		Matches:  matches,
	}
}

// buildContext returns a window of text around the match with relative offsets
// (UTF-16 code units), matching LanguageTool's context field.
func buildContext(text string, start, end int) MatchContext {
	const radius = 40
	s := start - radius
	if s < 0 {
		s = 0
	}
	e := end + radius
	if e > len(text) {
		e = len(text)
	}
	// Trim the window to sensible word boundaries when possible.
	window := text[s:e]
	return MatchContext{
		Text:   window,
		Offset: int64(start - s),
		Length: int64(end - start),
	}
}

func langName(lang string) string {
	if lang == "" {
		return "English (US)"
	}
	return lang
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]any{"error": fmt.Sprintf(format, args...)})
}