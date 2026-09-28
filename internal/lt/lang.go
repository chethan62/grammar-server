// Package lt holds the pure LanguageTool-compatible core: no I/O, no engine,
// no HTTP. Everything here is a value in, value out function so it can be unit
// tested in milliseconds.
package lt

import "sort"

// Language is one language this server can actually check.
type Language struct {
	Code    string // LanguageTool language code, e.g. "en-GB"
	Name    string // human name, as LanguageTool spells it
	Dialect string // the engine's dialect name
}

// Languages is the whole supported table, in the order clients see it.
//
// It is an exact table, not a substring match. The old dialectForLang() looked
// for "GB"/"CA"/"AU"/"IN" anywhere in the string and fell through to American,
// so language=de-DE returned HTTP 200, code "de-DE", and zero matches — a German
// document nobody checked, reported as clean.
var Languages = []Language{
	{Code: "en", Name: "English", Dialect: "American"},
	{Code: "en-US", Name: "English (US)", Dialect: "American"},
	{Code: "en-GB", Name: "English (UK)", Dialect: "British"},
	{Code: "en-CA", Name: "English (Canada)", Dialect: "Canadian"},
	{Code: "en-AU", Name: "English (Australia)", Dialect: "Australian"},
	{Code: "en-IN", Name: "English (India)", Dialect: "Indian"},
}

var byCode = func() map[string]Language {
	m := make(map[string]Language, len(Languages))
	for _, l := range Languages {
		m[l.Code] = l
	}
	return m
}()

// Supported reports whether code is a language this server can check.
func Supported(code string) bool {
	_, ok := byCode[code]
	return ok
}

// Canonical normalises an accepted code. An omitted language still means
// American English (that is what the endpoint did before this table existed,
// and clients that omit it keep working); "en" is LanguageTool's alias for it.
func Canonical(code string) string {
	if code == "" || code == "en" {
		return "en-US"
	}
	return code
}

// Lookup returns the entry for a code, canonicalising first. The bool is false
// for a language we cannot check.
func Lookup(code string) (Language, bool) {
	l, ok := byCode[Canonical(code)]
	return l, ok
}

// SupportedCodes lists the accepted codes, sorted, for error messages and for
// GET /v2/languages.
func SupportedCodes() []string {
	codes := make([]string, 0, len(Languages))
	for _, l := range Languages {
		codes = append(codes, l.Code)
	}
	sort.Strings(codes)
	return codes
}
