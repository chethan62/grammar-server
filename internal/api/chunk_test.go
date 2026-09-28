package api

import (
	"strings"
	"testing"
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
