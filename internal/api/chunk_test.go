package api

import (
	"reflect"
	"strings"
	"testing"

	"grammar-server/internal/engine"
)

// The chunker is the only thing standing between a long document and the LSP
// deadline, so it gets its own check: segments must cover the text exactly, in
// order, without overlap, and never grow past one chunk.
func TestSentenceSegments(t *testing.T) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 2000)
	segs := sentenceSegments(text)
	if len(segs) < 5 {
		t.Fatalf("expected several segments, got %d", len(segs))
	}
	if segs[0][0] != 0 || segs[len(segs)-1][1] != len(text) {
		t.Fatalf("segments do not cover the text: %v … %v", segs[0], segs[len(segs)-1])
	}
	var joined strings.Builder
	for i, s := range segs {
		if s[1] <= s[0] {
			t.Fatalf("segment %d is empty: %v", i, s)
		}
		if i > 0 && segs[i-1][1] != s[0] {
			t.Fatalf("gap or overlap between %v and %v", segs[i-1], s)
		}
		if n := len(text[s[0]:s[1]]); n > chunkBytes {
			t.Fatalf("segment %d holds %d bytes, over the %d chunk size", i, n, chunkBytes)
		}
		joined.WriteString(text[s[0]:s[1]])
	}
	if joined.String() != text {
		t.Fatal("segments do not reassemble the original text")
	}

	// A single sentence longer than a chunk has no boundary to cut at and must
	// still be cut, or one giant paragraph would bypass the whole scheme.
	long := strings.Repeat("a", chunkBytes*3)
	segs = sentenceSegments(long)
	if len(segs) < 2 {
		t.Fatalf("a sentence longer than a chunk must still be cut, got %d segment(s)", len(segs))
	}

	// Short text stays whole, and so does a text that ends without punctuation.
	short := "One sentence."
	if segs := sentenceSegments(short); len(segs) != 1 || segs[0] != [2]int{0, len(short)} {
		t.Fatalf("short text should stay whole, got %v", segs)
	}
	tail := strings.Repeat("word ", 10)
	if segs := sentenceSegments(tail); len(segs) != 1 || segs[0][1] != len(tail) {
		t.Fatalf("text with no sentence end should stay whole, got %v", segs)
	}
}

// A chunk boundary inside a word makes the engine report both halves as
// misspellings: "over" cut at a 12 KB boundary came back as 'o' and 'ver' on a
// text that had nothing wrong with it. Without sentence punctuation to cut on
// (bullet lists, tables, comma run-ons, scripts that do not use '.'),
// the boundary must fall on whitespace.
func TestChunkBoundariesFallOnWhitespace(t *testing.T) {
	texts := map[string]string{
		"no punctuation at all": strings.Repeat("alpha bravo charlie delta echo ", 2000),
		"bulleted list":         strings.Repeat("item one here\n", 2000),
		"comma run-on":          strings.Repeat("first, second, third, fourth, ", 1500),
		"tabs and newlines":     strings.Repeat("col one	col two	col three\r\n", 1500),
	}
	for name, text := range texts {
		t.Run(name, func(t *testing.T) {
			segs := sentenceSegments(text)
			if len(segs) < 3 {
				t.Fatalf("expected several segments, got %d", len(segs))
			}
			for i, s := range segs {
				if s[1] == len(text) {
					continue // the end of the text is not a cut
				}
				if end := text[s[1]-1]; end != ' ' && end != '	' && end != '\n' && end != '\r' {
					t.Errorf("segment %d ends mid-word at %q: %q…", i, end, text[s[1]-20:s[1]+8])
				}
				if i > 0 && text[s[0]] == ' ' {
					t.Errorf("segment %d starts with a space", i)
				}
			}
		})
	}
}

// checkChunked had no test of its own contract, and the offsets are the contract:
// a real error in the fourth chunk has to be reported where the client's whole
// document has it, and no match may cover half a word.
func TestChunkedOffsetsSurviveTheShift(t *testing.T) {
	eng, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer eng.Close()

	// Ordinary prose: sentence ends to cut on. (A text with no sentence end at all
	// makes harper flag each chunk as one long sentence, and that match overlaps and
	// swallows everything inside it — a separate matter from offsets.)
	base := strings.Repeat("The quick brown fox jumps over a lazy dog. ", 700)
	text := base[:len(base)-60] + " The report was teh one. " + base[len(base)-60:]
	at := strings.Index(text, " teh ")
	if at < chunkBytes*2 {
		t.Fatalf("the typo must sit well past the first chunk, it is at %d", at)
	}

	srv := NewServer(eng)
	lints, err := srv.checkChunked(text)
	if err != nil {
		t.Fatal(err)
	}
	// ASCII throughout, so byte offset, rune offset and UTF-16 offset agree.
	found := false
	for _, l := range lints {
		if l.CharStart == at+1 && l.CharEnd == at+4 && text[l.CharStart:l.CharEnd] == "teh" {
			found = true
		}
		// No match may be half a word: that is what a 12 KB cut inside a word
		// breaks ("over" came back as 'o' and 'ver'). A match that is whitespace
		// or punctuation is fine — what is not fine is a match that starts or
		// ends between two letters.
		mt := text[l.CharStart:l.CharEnd]
		if mt == "" {
			t.Errorf("empty match at %d", l.CharStart)
			continue
		}
		if isLetter(mt[0]) && l.CharStart > 0 && isLetter(text[l.CharStart-1]) {
			t.Errorf("match %q at %d starts inside a word", mt, l.CharStart)
		}
		if isLetter(mt[len(mt)-1]) && l.CharEnd < len(text) && isLetter(text[l.CharEnd]) {
			t.Errorf("match %q at %d ends inside a word", mt, l.CharStart)
		}
	}
	if !found {
		for _, l := range lints {
			t.Logf("lint %-32s %6d-%6d %q", l.Rule, l.CharStart, l.CharEnd, text[max(0, l.CharStart):min(l.CharEnd, len(text))])
		}
		t.Errorf("the planted typo at %d was not reported; got %d lints", at+1, len(lints))
	}

	// The same document again: every chunk is still cached, so this pass takes the
	// hit path and has to produce exactly the same lints. A cache that changes the
	// answer is worse than no cache, and this is the assertion that says so.
	again, err := srv.checkChunked(text)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lints, again) {
		t.Fatalf("the cached pass differs from the first: %d lints vs %d", len(again), len(lints))
	}
	if entries, hits, _ := srv.cache.stats(); entries == 0 || hits == 0 {
		t.Fatalf("the second pass did not come from the cache: %d entries, %d hits", entries, hits)
	}
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
