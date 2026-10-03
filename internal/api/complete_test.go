package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The ranking is the part of a completion that can be wrong without looking wrong: a word the user taught the
// engine has to come back before harper's own list, and the two sources must not repeat a word between them.
func TestCompletionsPutTheMachinesOwnWordsFirst(t *testing.T) {
	mine := []string{"specular", "WebKitGTK"}
	// The possessive sits in the MIDDLE of harper's list on purpose: a plain scan would return it second, and
	// this is the fixture that fails if the possessive pass is ever removed.
	harper := []string{"specialist", "speck's", "specials", "specific", "speck", "specify"}

	got := completions("spec", mine, harper, 4)
	want := []string{"specular", "specialist", "specials", "specific"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("completions(spec) = %v, want %v — the taught word first, then harper's own words before any possessive", got, want)
	}
	// And nothing is dropped for carrying an apostrophe: it just comes last.
	if got := completions("spec", mine, harper, 25); got[len(got)-1] != "speck's" {
		t.Fatalf("a possessive must still be offered, at the end: %v", got)
	}
	// The limit holds even when both sources could fill it.
	if got := completions("spec", mine, harper, 2); len(got) != 2 {
		t.Fatalf("the limit was ignored: %v", got)
	}
	// A word in both sources appears once, spelled the way this machine has it.
	dupe := completions("webkit", []string{"WebKitGTK"}, []string{"webkitgtk", "WebKit"}, 5)
	if len(dupe) != 2 || dupe[0] != "WebKitGTK" || dupe[1] != "WebKit" {
		t.Fatalf("a word in both sources must appear once, in the machine's spelling: %v", dupe)
	}
	// Matching ignores case: "Spec" asks the same question as "spec".
	if got := completions("spec", nil, []string{"SPECIAL"}, 5); len(got) != 1 || got[0] != "SPECIAL" {
		t.Fatalf("a capitalised word must match a lowercased prefix: %v", got)
	}
	// Nothing matches: an empty list, not an error and not the whole dictionary.
	if got := completions("zzzz", mine, harper, 5); len(got) != 0 {
		t.Fatalf("no match must be empty, got %v", got)
	}
}

func TestCompleteAnswersAOneLetterPrefixWithoutAnEngine(t *testing.T) {
	// One letter is the alphabet, not a completion — and saying so must not need harper, which is what a
	// Server with no engine at all proves here: reaching for one would panic.
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/v2/complete?prefix=s", nil)
	w := httptest.NewRecorder()
	s.handleComplete(w, req)
	if w.Code != 200 {
		t.Fatalf("a one-letter prefix must be answered, not refused with %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"count":0`) {
		t.Fatalf("a one-letter prefix must offer nothing: %s", w.Body)
	}
	// An absent prefix is the same question with less information.
	req = httptest.NewRequest(http.MethodGet, "/v2/complete", nil)
	w = httptest.NewRecorder()
	s.handleComplete(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"count":0`) {
		t.Fatalf("no prefix must answer with nothing, got %d: %s", w.Code, w.Body)
	}
}
