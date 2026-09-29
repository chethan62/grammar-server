package api

import (
	"net/http"
	"strings"

	"grammar-server/internal/lt"
)

// checkLang validates a client-supplied language code and returns the table
// entry to use for it. On refusal it writes LanguageTool's own error shape
// (plain text, the codes we know) and the caller must return immediately.
//
// One copy of the message on purpose: the code list is built from the same
// table the check path uses, so a language can never be advertised and refused
// at the same time.
func checkLang(w http.ResponseWriter, code string) (lt.Language, bool) {
	l, ok := lt.Lookup(code)
	if !ok {
		writeLTError(w, http.StatusBadRequest,
			"'%s' is not a language code known to grammar-server. Supported language codes are: %s",
			code, strings.Join(lt.SupportedCodes(), ", "))
		return lt.Language{}, false
	}
	return l, true
}

// languageInfo is the language a response advertises: the canonical table entry
// for the requested code, so the body can never name a code the engine did not
// check ("en" is reported as "en-US", not echoed back as "en").
//
// An unknown code is unreachable here — /v2/check refuses it first — but if a
// future caller passes one, fall back to the default rather than echo it.
func languageInfo(code string) LangInfo {
	l, ok := lt.Lookup(code)
	if !ok {
		l, _ = lt.Lookup("")
	}
	return langInfo(l)
}
