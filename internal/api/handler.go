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

// ruleList accepts both LanguageTool encodings of a rule list: a JSON array
// ({"enabledRules":["The"]}) and a comma-separated string ("The,SpellCheck"),
// which is what form-encoded/query clients send.
type ruleList []string

func (l *ruleList) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = splitRules(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*l = arr
	return nil
}

// splitRules splits a comma-separated rule list, trimming blanks.
func splitRules(s string) ruleList {
	var out ruleList
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// CheckRequest mirrors the LanguageTool /v2/check request body. It is read
// from a JSON body, a form-encoded POST, or GET query parameters — the three
// shapes real LanguageTool clients send.
type CheckRequest struct {
	Text          string   `json:"text"`
	Language      string   `json:"language"`
	EnabledRules  ruleList `json:"enabledRules"`  // if non-empty, return only these
	DisabledRules ruleList `json:"disabledRules"` // never report these
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

// maxTextChars caps a single /v2/check body. Past it the engine's cost is
// unbounded in practice: a 200 KB document ran >47 s and kept harper-ls at 96%
// CPU after the client gave up. LanguageTool's own public limit is 20,000
// characters — we accept 10x that (and chunk above a chunk size later), so
// anything LT accepts works here too.
const maxTextChars = 200_000

// writeLTError replies the way LanguageTool does: plain text, "Error: " prefix.
// Clients show the body to the user as-is, so the shape is part of the contract.
func writeLTError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, "Error: %s\n", fmt.Sprintf(format, args...))
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Cheap early exit on a declared huge body; MaxBytesReader still guards a
	// chunked body that declares nothing.
	if r.ContentLength > maxTextChars*4+4096 {
		writeLTError(w, http.StatusRequestEntityTooLarge,
			"Your text exceeds the limit of %d characters. Please submit a shorter text.", maxTextChars)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTextChars*4+4096) // 4 bytes/char is generous
	req, err := parseCheckRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}
	if n := len([]rune(req.Text)); n > maxTextChars {
		writeLTError(w, http.StatusRequestEntityTooLarge,
			"Your text exceeds the limit of %d characters (it's %d characters). Please submit a shorter text.",
			maxTextChars, n)
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

// parseCheckRequest reads the three request shapes LanguageTool clients use:
// a JSON body, an application/x-www-form-urlencoded POST, and GET query
// parameters. LanguageTool's own API accepts all three.
func parseCheckRequest(r *http.Request) (CheckRequest, error) {
	var req CheckRequest
	media := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case r.Method == http.MethodGet || strings.HasPrefix(media, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			return req, fmt.Errorf("invalid form data: %w", err)
		}
		req.Text = r.Form.Get("text")
		req.Language = r.Form.Get("language")
		req.MotherTongue = r.Form.Get("motherTongue")
		req.EnabledRules = splitRules(r.Form.Get("enabledRules"))
		req.DisabledRules = splitRules(r.Form.Get("disabledRules"))
		return req, nil
	default:
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return req, fmt.Errorf("invalid JSON: %w", err)
		}
		return req, nil
	}
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

	// Sentence ranges are UTF-16 offsets over the whole text; each match
	// reports the sentence it falls in (LanguageTool always populates this).
	ranges := sentenceRanges(req.Text)

	matches := make([]Match, 0, len(lints))
	for _, l := range lints {
		// Clients filter by either the LanguageTool id (MORFOLOGIK_RULE_EN_US)
		// or harper's native name (SpellCheck).
		rule := ltRuleFor(l)
		if disabledAny(disabled, rule.ID, l.Rule) {
			continue
		}
		if useEnable && !enabledAny(enabled, rule.ID, l.Rule) {
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
			ShortMessage: rule.Short,
			Sentence:     sentenceAt(req.Text, ranges, l.CharStart),
			Replacements: reps,
			Context:      ctx,
			Rule: RuleInfo{
				ID:          rule.ID,
				Description: firstNonEmpty(rule.Description, l.Message),
				IssueType:   rule.IssueType,
				Category:    rule.Category,
			},
			Type: TypeInfo{TypeName: rule.TypeName},
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
		SentenceRanges: ranges,
	}
}

// enabledAny/disabledAny match a filter entry against both rule id spellings.
func enabledAny(enabled map[string]bool, ltID, harperID string) bool {
	return enabled[ltID] || enabled[harperID]
}

func disabledAny(disabled map[string]bool, ltID, harperID string) bool {
	return disabled[ltID] || disabled[harperID]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sentenceAt returns the sentence containing the UTF-16 offset pos, using the
// precomputed ranges. Falls back to the whole text if no range matches.
func sentenceAt(text string, ranges [][]int64, pos int) string {
	for _, r := range ranges {
		if len(r) != 2 {
			continue
		}
		if pos >= int(r[0]) && pos < int(r[1]) {
			return text[u16ToByte(text, int(r[0])):u16ToByte(text, int(r[1]))]
		}
	}
	return text
}

// buildContext returns a window of text around the match with relative offsets
// (UTF-16 code units), matching LanguageTool's context field. The window bounds
// are UTF-16 offsets and must be converted to byte offsets before slicing:
// slicing a UTF-8 string at UTF-16 positions cuts mid-rune on non-ASCII text
// and shifts the reported context.offset.
func buildContext(text string, start, end int) MatchContext {
	const radius = 40
	s := start - radius
	if s < 0 {
		s = 0
	}
	e := end + radius
	if e > u16Len(text) {
		e = u16Len(text)
	}
	window := text[u16ToByte(text, s):u16ToByte(text, e)]
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

// u16Offset returns the UTF-16 code unit offset of a byte position in text.
func u16Offset(text string, byteOff int) int {
	if byteOff <= 0 {
		return 0
	}
	n := 0
	for i, r := range text {
		if i >= byteOff {
			break
		}
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// u16ToByte converts a UTF-16 code unit offset into a byte offset in text,
// so UTF-16 positions can be used to slice the UTF-8 string safely.
func u16ToByte(text string, u16off int) int {
	if u16off <= 0 {
		return 0
	}
	n := 0
	for i, r := range text {
		if n >= u16off {
			return i
		}
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return len(text)
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
	r.Body = http.MaxBytesReader(w, r.Body, maxTextChars*4+4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}
	if n := len([]rune(req.Text)); n > maxTextChars {
		writeLTError(w, http.StatusRequestEntityTooLarge,
			"Your text exceeds the limit of %d characters (it's %d characters). Please submit a shorter text.",
			maxTextChars, n)
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
		if len(l.Replacements) == 0 || l.CharStart < 0 {
			continue
		}
		// CharStart/CharEnd are UTF-16 code units; convert to byte offsets
		// before slicing. Treating them as byte positions corrupts any
		// non-ASCII text (the bytes of a preceding é shift every boundary).
		// Processing is back-to-front, so the prefix up to CharStart is
		// still the original text and the conversion stays valid.
		start := u16ToByte(string(out), l.CharStart)
		end := u16ToByte(string(out), l.CharEnd)
		if start >= end || end > len(out) {
			continue
		}
		out = append(out[:start], append([]byte(l.Replacements[0]), out[end:]...)...)
	}
	return string(out)
}