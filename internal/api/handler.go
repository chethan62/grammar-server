// Package api implements the LanguageTool-compatible HTTP endpoints.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"grammar-server/internal/engine"
	"grammar-server/internal/lt"
	"grammar-server/internal/rewrite"
)

// ruleList accepts both LanguageTool encodings of a list parameter — rules,
// categories, preferred variants: a JSON array ({"enabledRules":["The"]}) and a
// comma-separated string ("The,SpellCheck"), which is what form-encoded and query
// clients send. A plain string field 400s the array, which is the shape every
// JSON client uses.
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

// truthy reads a boolean the way form/query clients send it ("true", "1", …).
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// styleRequested reports whether the client asked for the style tier: the picky
// level, the STYLE category, or one of the two style rules by id. LanguageTool
// behaves the same way — its passive-voice rule fires only at level=picky.
func styleRequested(req CheckRequest) bool {
	if req.Level == "picky" {
		return true
	}
	for _, c := range req.EnabledCategories {
		if strings.EqualFold(c, "STYLE") {
			return true
		}
	}
	for _, r := range req.EnabledRules {
		if styleRuleIDs[r] {
			return true
		}
	}
	return false
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
	Text               string   `json:"text"`
	Language           string   `json:"language"`
	EnabledRules       ruleList `json:"enabledRules"`       // switch these on (with enabledOnly: only these)
	DisabledRules      ruleList `json:"disabledRules"`      // never report these
	EnabledCategories  ruleList `json:"enabledCategories"`  // switch whole categories on
	DisabledCategories ruleList `json:"disabledCategories"` // never report these categories
	EnabledOnly        bool     `json:"enabledOnly"`        // nothing but the rules/categories named above
	Level              string   `json:"level"`              // "" or "default"; "picky" adds the style tier
	MotherTongue       string   `json:"motherTongue"`       // accepted; no rule uses it yet
	PreferredVariants  ruleList `json:"preferredVariants"`  // spelling-variant preference, e.g. ["en-GB"]
}

// CheckResponse mirrors the LanguageTool /v2/check response.
type CheckResponse struct {
	Software       Software  `json:"software"`
	Language       LangInfo  `json:"language"`
	Matches        []Match   `json:"matches"`
	SentenceRanges [][]int64 `json:"sentenceRanges"`
	// ExtendedSentenceRanges is present in every LanguageTool response and carries
	// from/to plus per-sentence detectedLanguages. LanguageTool fills those by running
	// language detection; this server checks exactly one language and does not guess,
	// so each entry names the language the sentence was checked as. The field is here
	// because a client that maps over it cannot survive its absence — the same reason
	// longCode exists in LangInfo.
	ExtendedSentenceRanges []ExtendedSentenceRange `json:"extendedSentenceRanges"`
	Warnings               Warnings                `json:"warnings"`
}

// ExtendedSentenceRange is one entry of LanguageTool's extendedSentenceRanges.
type ExtendedSentenceRange struct {
	From              int64              `json:"from"`
	To                int64              `json:"to"`
	DetectedLanguages []DetectedLanguage `json:"detectedLanguages"`
}

type DetectedLanguage struct {
	Language string  `json:"language"`
	Rate     float64 `json:"rate"`
}

// Warnings is part of the LanguageTool response shape: clients read
// incompleteResults to learn whether the whole text was checked.
type Warnings struct {
	IncompleteResults bool `json:"incompleteResults"`
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
	// LongCode is the same tag in LanguageTool's long spelling. Clients read it:
	// language_tool_python builds its language set from code and longCode, and a
	// missing key makes it add None and crash before it can even check anything.
	LongCode string `json:"longCode"`
}

// langInfo is the one place a table entry becomes a response entry, so /v2/languages
// and a check's "language" object can never disagree about a code.
func langInfo(l lt.Language) LangInfo {
	return LangInfo{Name: l.Name, Code: l.Code, LongCode: l.Code}
}

type Match struct {
	Message      string        `json:"message"`
	ShortMessage string        `json:"shortMessage"` // always sent ("" when unmapped), as LanguageTool does
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

// Server owns the HTTP handlers and the backing engine.
type Server struct {
	eng     *engine.Harper
	version string

	// listen is the host:port this process was started with, reported by /status. It
	// is a property of how the binary was launched rather than of the engine, so main
	// sets it, and it is reported at all because who can reach this API is the one
	// fact a diagnostic about it cannot derive from anything else here.
	listen string

	// rw is nil unless a rewrite backend is configured. Every other endpoint
	// behaves identically while it is nil, which is the normal case.
	//
	// It is swappable at runtime (the UI can change backends without a restart),
	// so it is read under rwMu and never called directly. rwProvider/rwURL/rwModel
	// travel with it because the /v1/ai endpoint reports them, and a client
	// interface alone cannot name the backend it is.
	// cache holds the engine's verdict per (configuration, chunk), so a client
	// re-sending a document only pays for the parts that changed.
	cache *lintCache

	rwMu       sync.Mutex
	rw         rewrite.Rewriter
	rwProvider string
	rwURL      string
	rwModel    string
}

// Version is what /status and every response report. It is set at build time from
// the git tag (`make`, see the Makefile) so a released binary cannot claim a release
// it was not built from; "dev" means a build that did not go through make.
var Version = "dev"

func NewServer(eng *engine.Harper) *Server {
	return &Server{eng: eng, version: Version, cache: newLintCache(defaultCacheEntries)}
}

// SetListen records the host:port the process was launched with, so /status can
// report it. Empty when a server is built without one (tests).
func (s *Server) SetListen(addr string) { s.listen = addr }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", s.handleCheck)
	mux.HandleFunc("/v2/ignore", s.handleIgnore) // words to stop reporting (writes: this machine only)
	mux.HandleFunc("/v2/fix-sentence", s.handleFixSentence)
	mux.HandleFunc("/v2/rewrite", s.handleRewrite)
	mux.HandleFunc("/v2/stats", s.handleStats)
	mux.HandleFunc("/v2/languages", s.handleLanguages)
	mux.HandleFunc("/status", s.handleRoot)
	mux.HandleFunc("/v1/ai", s.handleAI) // read + set the rewrite backend (writes: this machine only)
	mux.HandleFunc("/", s.handleRoot)    // API index: the UI lives in its own repo now
	return logRequests(mux)
}

// handleRoot answers /status and / with the service name, version and the
// endpoints this build serves. It replaced the embedded single-page UI, which
// moved to its own repository (grammar-ui): the server is API-only now, so any
// UI — or none — can point at it.
func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	// The chunk cache earns its place only if its effect is visible; a hit rate
	// nobody can read is indistinguishable from a cache that never works.
	entries, hits, misses := s.cache.stats()
	writeJSON(w, 200, map[string]any{
		"cache":   map[string]any{"entries": entries, "hits": hits, "misses": misses},
		"service": "grammar-server",
		"status":  "OK",
		"version": s.version,
		"dialect": s.eng.Dialect(),
		// Who can reach this API belongs in the status: the default is loopback and
		// --host 0.0.0.0 puts every endpoint on the network, unauthenticated.
		"listen": s.listen,
		// How many words this server has been told to stop reporting. In the status because a
		// client on another machine cannot read the file, and "why is this word not flagged?"
		// deserves an answer that does not require being on the right host.
		"ignored": len(readIgnored(ignorePath())),
		"endpoints": []string{
			"POST /v2/check", "POST /v2/fix-sentence", "POST /v2/rewrite",
			"POST /v2/stats", "GET /v2/languages", "GET /status",
			"GET /v1/ai (what rewrite backend is configured)", "POST /v1/ai (change it, this machine only)",
			"POST /v2/ignore (words to stop reporting; this machine only)",
		},
		// There is no page to open. The clients live in the grammar-ui repo and are desktop
		// programs — a card at the caret, a selection checker on a shortcut, an AI-runner settings
		// panel — so "static; serve it" was advice that had been wrong since the browser UI was
		// deleted, and a client following it would have found nothing on that port.
		"ui": "https://github.com/chethan62/grammar-ui — desktop clients (Qt), not a page: a suggestion card at the caret, a selection checker, and the AI-runner settings panel. This API is reachable from anywhere on the network; changing the backend is not.",
	})
}

// handleLanguages serves the same table the check path validates against, so
// the two can never drift apart.
func (s *Server) handleLanguages(w http.ResponseWriter, _ *http.Request) {
	langs := make([]LangInfo, 0, len(lt.Languages))
	for _, l := range lt.Languages {
		langs = append(langs, langInfo(l))
	}
	writeJSON(w, 200, langs)
}

// maxTextChars caps a single /v2/check body, set from measurement rather than
// from a round number. The engine's cost is linear in characters and this box is
// CPU-only: 1 KB ~130 ms, 10 KB 1.8 s, 50 KB 8.8 s (docs/perf/baseline-*.json),
// so 100 KB is ~18 s. The cap has to fit inside the server's WriteTimeout with
// room to spare, or the two numbers contradict each other — a request that needs
// longer than the timeout is not a big-text promise, it is a dropped connection.
// That is exactly what a 200 KB body did: 48.4 s of work, then RemoteDisconnected
// with nothing in the log, because the handler was still working when the write
// timeout closed the socket. LanguageTool's own public limit is 20,000
// characters; 100 KB is 5x that, so anything an LT client sends still works.
const maxTextChars = 100_000

// The two limit messages, shared so the endpoints cannot drift apart. LanguageTool
// clients show these bodies verbatim.
const (
	ltTextTooLong  = "Your text exceeds the limit of %d characters. Please submit a shorter text."
	ltTextGot      = "Your text exceeds the limit of %d characters (it's %d characters). Please submit a shorter text."
	rewriteTooLong = "A rewrite is limited to %d characters. Rewrite one sentence or paragraph at a time."
	rewriteGot     = "A rewrite is limited to %d characters (it's %d). Rewrite one sentence or paragraph at a time."
)

// capBody applies the guard every text endpoint shares: refuse an over-declared
// body on its Content-Length before reading it, then cap the reader at 4 bytes per
// character (generous for UTF-8). msg takes the limit.
//
// The order is the point. The body cap is larger than the character cap, so when
// MaxBytesReader fires first the client gets the decoder's JSON 400 "request body
// too large" where LanguageTool answers a plain-text 413. /v2/rewrite had that bug
// and /v2/fix-sentence still did until both moved here.
func capBody(w http.ResponseWriter, r *http.Request, limit int, msg string) bool {
	if r.ContentLength > int64(limit)*4+4096 {
		writeLTError(w, http.StatusRequestEntityTooLarge, msg, limit)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit)*4+4096)
	return true
}

// textTooLong refuses text past the character limit in LanguageTool's plain-text
// shape, naming the size it got. msg takes the limit and the actual count.
func textTooLong(w http.ResponseWriter, text string, limit int, msg string) bool {
	n := len([]rune(text))
	if n <= limit {
		return false
	}
	writeLTError(w, http.StatusRequestEntityTooLarge, msg, limit, n)
	return true
}

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
	if !capBody(w, r, maxTextChars, ltTextTooLong) {
		return
	}
	req, err := parseCheckRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' is required")
		return
	}
	if textTooLong(w, req.Text, maxTextChars, ltTextGot) {
		return
	}
	// An omitted language still means American English, as it always did; an
	// unknown one is refused instead of being checked as English, which used to
	// return HTTP 200 with zero matches — a document nobody checked, reported clean.
	lang, ok := checkLang(w, req.Language)
	if !ok {
		return
	}

	if req.Level != "" && req.Level != "default" && req.Level != "picky" {
		writeLTError(w, http.StatusBadRequest, "level must be 'default' or 'picky', got '%s'", req.Level)
		return
	}
	// preferredVariants is LanguageTool's spelling-variant preference, so it is the
	// dialect: take the first entry we can check and ignore the rest, as LT ignores
	// variants it does not know.
	dialect := lang.Dialect
	for _, v := range req.PreferredVariants {
		if l, ok := lt.Lookup(v); ok {
			dialect = l.Dialect
			break
		}
	}
	if err := s.eng.SetDialect(dialect); err != nil {
		writeError(w, http.StatusInternalServerError, "set dialect: %v", err)
		return
	}
	// Rule toggles are engine state, not a result filter: enabledRules has to
	// reach harper's linter map or an off-by-default rule can never fire.
	enRules, disRules := harperRuleNames(req.EnabledRules), harperRuleNames(req.DisabledRules)
	if err := s.eng.SetRules(enRules, disRules, req.EnabledOnly && len(enRules) > 0); err != nil {
		writeError(w, http.StatusInternalServerError, "set rules: %v", err)
		return
	}
	// One engine call per ~12k characters: harper's cost grows with the document,
	// and a 200 KB text used to blow the LSP deadline.
	lints, err := s.checkChunked(req.Text)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "engine error: %v", err)
		return
	}
	// The engine has no rule for wordiness or passive voice (see
	// internal/lt/style.go); add those hints here so they travel through the same
	// rule mapping, replacements and filter as everything else. Opt-in, like
	// LanguageTool's picky level: default output stays the correctness tier.
	if styleRequested(req) {
		lints = withStyleLints(req.Text, lints)
	}

	resp := s.buildResponse(req, lints)
	writeJSON(w, 200, resp)
}

// checkedNothing reports whether the request asked for a check that cannot find
// anything: enabledOnly naming nothing this engine knows. harper answers that with
// an empty match list, and a client renders it as a green "no issues" — the same
// silent wrong answer the language check refuses to give for a code it cannot
// check. LanguageTool's own field for "this result is not the whole story" is
// warnings.incompleteResults, so that is what a client is told.
//
// An empty result from a request that DID name something known stays false: no
// matches then means the text is clean, which is the honest answer.
func (s *Server) checkedNothing(req CheckRequest) bool {
	if !req.EnabledOnly {
		return false
	}
	for _, name := range harperRuleNames(req.EnabledRules) {
		if s.eng.KnowsRule(name) {
			return false
		}
	}
	for _, id := range req.EnabledCategories {
		if categoriesWeEmit[strings.ToUpper(id)] {
			return false
		}
	}
	log.Printf("enabledOnly named no rule or category this engine has "+
		"(enabledRules=%v enabledCategories=%v): not the same as a clean text",
		req.EnabledRules, req.EnabledCategories)
	return true
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
		req.Level = r.Form.Get("level")
		req.MotherTongue = r.Form.Get("motherTongue")
		req.PreferredVariants = splitRules(r.Form.Get("preferredVariants"))
		req.EnabledOnly = truthy(r.Form.Get("enabledOnly"))
		req.EnabledRules = splitRules(r.Form.Get("enabledRules"))
		req.DisabledRules = splitRules(r.Form.Get("disabledRules"))
		req.EnabledCategories = splitRules(r.Form.Get("enabledCategories"))
		req.DisabledCategories = splitRules(r.Form.Get("disabledCategories"))
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
	enabledCat := map[string]bool{}
	for _, id := range req.EnabledCategories {
		if id != "" {
			enabledCat[strings.ToUpper(id)] = true
		}
	}
	disabledCat := map[string]bool{}
	for _, id := range req.DisabledCategories {
		if id != "" {
			disabledCat[strings.ToUpper(id)] = true
		}
	}
	// enabledOnly means "nothing but what I named" — by rules, by categories, or
	// both, whichever the client sent. Plain enabledRules just adds rules.
	useEnable := req.EnabledOnly && len(req.EnabledRules) > 0
	useEnableCat := req.EnabledOnly && len(req.EnabledCategories) > 0

	// Sentence ranges are UTF-16 offsets over the whole text; each match
	// reports the sentence it falls in (LanguageTool always populates this).
	ranges := sentenceRanges(req.Text)

	// The ignore list is read per check rather than cached: it is a few hundred bytes, the path is
	// part of the server's config, and a cache here would need invalidating by whoever writes the
	// file. ponytail: one file read per check; cache it on mtime if /v2/check ever shows it.
	ignored := readIgnored(ignorePath())

	matches := make([]Match, 0, len(lints))
	for _, l := range lints {
		// Clients filter by either the LanguageTool id (MORFOLOGIK_RULE_EN_US)
		// or harper's native name (SpellCheck).
		rule := ltRuleFor(l)
		if disabledAny(disabled, rule.ID, l.Rule) || disabledCat[strings.ToUpper(rule.Category.ID)] {
			continue
		}
		if useEnable && !enabledAny(enabled, rule.ID, l.Rule) {
			continue
		}
		if useEnableCat && !enabledCat[strings.ToUpper(rule.Category.ID)] {
			continue
		}
		// Last, so an ignored word and a rule filter never interact — and here rather than in a
		// client, because this is the one place every client's matches come through.
		if ignoredText(req.Text, l.CharStart, l.CharEnd, ignored) {
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
	// Clients render matches in the order they arrive and highlight with offsets,
	// so the order has to be the document's order — harper's own order is not.
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Offset < matches[j].Offset })
	return CheckResponse{
		Software: Software{
			Name: "grammar-server", Version: s.version,
			BuildDate:  time.Now().UTC().Format(time.RFC3339),
			APIVersion: 2, Status: "OK",
		},
		Language:               languageInfo(req.Language),
		Matches:                matches,
		SentenceRanges:         ranges,
		ExtendedSentenceRanges: extendedRanges(ranges, req.Language),
		Warnings:               Warnings{IncompleteResults: s.checkedNothing(req)},
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

// extendedRanges wraps the same ranges LanguageTool returns in sentenceRanges, with
// the language every sentence was checked as. Rate is 1.0: one language was used for
// the whole request and there is nothing to be uncertain about.
func extendedRanges(ranges [][]int64, language string) []ExtendedSentenceRange {
	base := language
	if i := strings.IndexByte(base, '-'); i > 0 {
		base = base[:i]
	}
	out := make([]ExtendedSentenceRange, 0, len(ranges))
	for _, r := range ranges {
		if len(r) != 2 {
			continue
		}
		out = append(out, ExtendedSentenceRange{
			From: r[0], To: r[1],
			DetectedLanguages: []DetectedLanguage{{Language: base, Rate: 1.0}},
		})
	}
	return out
}

// sentenceRanges returns UTF-16 code unit offset pairs [start, end) for the
// sentences in text, from the one sentence definition this codebase has
// (lt.SentenceRanges). It used to scan for periods here as well, which is how the
// stats and the API could disagree about where a sentence starts ("Dr. Smith").
func sentenceRanges(text string) [][]int64 {
	ranges := lt.SentenceRanges(text)
	out := make([][]int64, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, []int64{int64(u16Offset(text, r[0])), int64(u16Offset(text, r[1]))})
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

// leavesNamesAlone reports whether a spelling suggestion is one to leave alone: a
// single letter ("R. K. Rao" — harper suggests "RI" for "R.") or a capitalised word
// that is not the first in the text ("Rao" → "Rad"). Applying those rewrites names,
// which is not a checker's call; the sentence's other suggestions still apply, so
// "She go to the office." still becomes "She goes to the office." Grammar and
// typography suggestions are unaffected — only spelling guesses.
func leavesNamesAlone(text string, l engine.Lint) bool {
	// The category the client sees, not the engine's internal kind: the LT mapping
	// is what makes "this is a possible typo" a stable statement.
	if ltRuleFor(l).Category.ID != "TYPOS" {
		return false
	}
	mt := text[u16ToByte(text, l.CharStart):u16ToByte(text, l.CharEnd)]
	// An initial is a single letter with its dot attached: harper matches "R." as a
	// two-rune typo, so count the letters, not the runes.
	letters := strings.TrimFunc(mt, func(r rune) bool { return !unicode.IsLetter(r) })
	if utf8.RuneCountInString(letters) <= 1 {
		return true
	}
	r, _ := utf8.DecodeRuneInString(letters)
	return unicode.IsUpper(r) && l.CharStart > 0
}

// sentenceAround returns the sentence containing a BYTE offset, and that sentence's
// byte range. A client's offset is UTF-16 and must be converted first (u16ToByte).
//
// The boundary comes from lt.SentenceRanges, the same one the stats and the
// response fields use, so "Dr. Smith wrote it." is one sentence here too and the
// answer can never be a fragment the caller would replace on its own.
func sentenceAround(text string, offset int) (string, [2]int) {
	for _, r := range lt.SentenceRanges(text) {
		if offset >= r[0] && offset < r[1] {
			return text[r[0]:r[1]], r
		}
	}
	return text, [2]int{0, len(text)}
}

// --- Fix sentence (rule-based, no AI) --------------------------------

// FixSentenceRequest is the body for /v2/fix-sentence. Offset is a UTF-16 code
// unit offset, like every other offset in this API (LanguageTool's convention).
type FixSentenceRequest struct {
	Text   string `json:"text"`
	Offset int    `json:"offset"`
}

// FixSentenceResponse is the result from /v2/fix-sentence. Offset and Length are
// the UTF-16 range Fixed belongs to, so a client replaces exactly the text the
// server fixed instead of computing the sentence boundary a second time — two
// implementations of that boundary is how a fix lands on the wrong sentence.
type FixSentenceResponse struct {
	Fixed  string `json:"fixed"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

func (s *Server) handleFixSentence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !capBody(w, r, maxTextChars, ltTextTooLong) {
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
	if textTooLong(w, req.Text, maxTextChars, ltTextGot) {
		return
	}
	// req.Offset is the client's offset, and the API speaks UTF-16 code units
	// (LanguageTool's convention). Slicing the UTF-8 string with it directly was
	// the bug: with an emoji before the error the byte index lands two bytes
	// early, extractSentence returns the PREVIOUS sentence, and the UI swaps that
	// sentence into the document. Convert once, here, and extractSentence works
	// in byte offsets like the rest of the package.
	// req.Offset is a UTF-16 code unit offset (LanguageTool's convention) and the
	// sentence lookup works in bytes: reading it as a byte index was the bug that
	// fixed the PREVIOUS sentence whenever the text had an emoji or an accent
	// before the error, and the UI swapped that sentence into the document.
	sentence, span := sentenceAround(req.Text, u16ToByte(req.Text, req.Offset))

	// Run harper on just the sentence to get lints.
	lints, err := s.eng.Check(sentence)
	fixed := sentence
	if err == nil && len(lints) > 0 {
		// Apply first-replacement suggestions back-to-front (offsets stay valid).
		fixed = applyFixes(sentence, lints)
	}
	// The range travels with the answer: the client replaces [offset, offset+length)
	// and never has to guess which sentence the server meant.
	start := u16Offset(req.Text, span[0])
	writeJSON(w, 200, FixSentenceResponse{
		Fixed:  fixed,
		Offset: int64(start),
		Length: int64(u16Offset(req.Text, span[1]) - start),
	})
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
		if leavesNamesAlone(text, *l) {
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
