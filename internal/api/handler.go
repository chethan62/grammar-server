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
	return &Server{eng: eng, version: "0.2.0"}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", s.handleCheck)
	mux.HandleFunc("/v2/fix-sentence", s.handleFixSentence)
	mux.HandleFunc("/v2/languages", s.handleLanguages)
	mux.HandleFunc("/status", s.handleRoot)   // old health endpoint
	mux.HandleFunc("/", s.serveUI)            // single-page UI
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
		seen := map[string]bool{}
		reps := make([]Replacement, 0, len(l.Replacements))
		for _, rep := range l.Replacements {
			if rep != "" && !seen[rep] {
				reps = append(reps, Replacement{Value: rep})
				seen[rep] = true
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
		Language:       LangInfo{Name: langName(req.Language), Code: langCode(req.Language)},
		Matches:        matches,
		SentenceRanges: sentenceRanges(req.Text),
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

// --- Sentence helpers -------------------------------------------------

// sentenceRanges splits text into sentences and returns UTF-16 code unit
// offset pairs [start, end) for each sentence.
func sentenceRanges(text string) [][]int64 {
	var out [][]int64
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '.' || c == '!' || c == '?' {
			end := i + 1 // include punctuation
			// skip trailing space
			for end < len(text) && text[end] == ' ' {
				end++
			}
			out = append(out, []int64{int64(u16Offset(text, start)), int64(u16Offset(text, end))})
			start = end
			i = end - 1 // skip ahead
		} else if c == '\n' && i+1 < len(text) && text[i+1] == '\n' {
			end := i + 1
			out = append(out, []int64{int64(u16Offset(text, start)), int64(u16Offset(text, end))})
			start = end
			i = end - 1
		}
	}
	// trailing text after last punctuation
	if start < len(text) {
		end := len(text)
		for end > start && (text[end-1] == ' ' || text[end-1] == '\n') {
			end--
		}
		if end > start {
			out = append(out, []int64{int64(u16Offset(text, start)), int64(u16Offset(text, end))})
		}
	}
	if len(out) == 0 {
		out = append(out, []int64{0, int64(u16Len(text))})
	}
	return out
}

// u16Len returns the UTF-16 code unit length of a string.
func u16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// u16Offset returns the UTF-16 code unit offset of the byte position in text.
func u16Offset(text string, byteOff int) int {
	n := 0
	for _, r := range text {
		if n >= byteOff {
			break
		}
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
		if n > byteOff {
			break // partial rune (shouldn't happen with ASCII)
		}
	}
	return n
}

func extractSentence(text string, offset int) string {
	if offset < 0 || offset >= len(text) {
		return text
	}
	s := offset
	for s > 0 && text[s-1] != '.' && text[s-1] != '!' && text[s-1] != '?' && text[s-1] != '\n' {
		s--
	}
	e := offset
	for e < len(text) && text[e] != '.' && text[e] != '!' && text[e] != '?' {
		if text[e] == '\n' && e > offset {
			break
		}
		e++
	}
	if e < len(text) && (text[e] == '.' || text[e] == '!' || text[e] == '?') {
		e++
	}
	for s < e && (text[s] == ' ' || text[s] == '.' || text[s] == '!' || text[s] == '?') {
		s++
	}
	return text[s:e]
}

// --- Fix sentence (rule-based, no AI) --------------------------------

// FixSentenceRequest is the body for /v2/fix-sentence.
type FixSentenceRequest struct {
	Text   string `json:"text"`
	Offset int    `json:"offset"` // byte offset of the error; the containing sentence is extracted
}

// FixSentenceResponse is the result from /v2/fix-sentence.
type FixSentenceResponse struct {
	Fixed string `json:"fixed"`
}

func (s *Server) handleFixSentence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req FixSentenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}
	sentence := extractSentence(req.Text, req.Offset)

	// Run harper on just the sentence to get lints.
	lints, err := s.eng.Check(sentence)
	if err != nil || len(lints) == 0 {
		writeJSON(w, 200, FixSentenceResponse{Fixed: sentence})
		return
	}

	// Apply first-replacement suggestions back-to-front (offsets stay valid).
	fixed := applyFixes(sentence, lints)
	writeJSON(w, 200, FixSentenceResponse{Fixed: fixed})
}

// applyFixes applies the first replacement of each lint to the text,
// processing from the end so earlier offsets remain valid.
func applyFixes(text string, lints []engine.Lint) string {
	// Sort back-to-front by offset
	sorted := make([]engine.Lint, len(lints))
	copy(sorted, lints)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].CharStart > sorted[i].CharStart {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	out := []byte(text)
	for i := range sorted {
		l := &sorted[i]
		if len(l.Replacements) == 0 || l.CharStart < 0 || l.CharEnd > len(out) {
			continue
		}
		rep := l.Replacements[0]
		// Convert UTF-16 offsets to byte offsets (both are equivalent for ASCII/BMP)
		// CharStart/CharEnd are UTF-16 units; for the single-sentence context and
		// English, these match byte positions.
		start, end := l.CharStart, l.CharEnd
		if start > end || start > len(out) {
			continue
		}
		out = append(out[:start], append([]byte(rep), out[end:]...)...)
	}
	return string(out)
}