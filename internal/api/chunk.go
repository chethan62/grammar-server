package api

import (
	"fmt"
	"unicode/utf8"

	"grammar-server/internal/engine"
)

// chunkBytes bounds one engine call. harper's cost tracks characters, and a byte
// ceiling is also a rune ceiling, so bounding bytes needs no rune counting to
// stay exact. It exists because harper's cost grows with the document: a 200 KB
// text ran past 47 s and kept harper-ls at 96% CPU after the client had given
// up, which the 15 s LSP deadline turns into a 500 — chunking is what keeps the
// 200k character cap an honest promise.
//
// ponytail: sequential chunks of 12 KB. Measured ~4 ms per KB, so one chunk is
// ~50 ms and a full 200k document ~1 s. If that becomes the bottleneck, pipeline
// the chunks through the engine rather than raising this number.
const chunkBytes = 12_000

// checkChunked lints text of any length by splitting it at sentence boundaries
// and shifting each chunk's offsets back to the whole document.
//
// Splitting only at sentence ends means no word is ever cut in half, and a match
// that still straddles a boundary is dropped rather than reported at an offset
// that points at the wrong characters.
func checkChunked(eng *engine.Harper, text string) ([]engine.Lint, error) {
	if len(text) <= chunkBytes {
		return eng.Check(text)
	}
	var out []engine.Lint
	seen := map[string]bool{}
	for _, seg := range sentenceSegments(text) {
		lints, err := eng.Check(text[seg[0]:seg[1]])
		if err != nil {
			return nil, err
		}
		// Chunk offsets are UTF-16 code units relative to the chunk; clients
		// highlight in the whole document, so shift them by the chunk's position.
		shift := u16Offset(text, seg[0])
		end := u16Offset(text, seg[1])
		for _, l := range lints {
			l.CharStart += shift
			l.CharEnd += shift
			if l.CharStart < shift || l.CharEnd > end {
				continue
			}
			key := fmt.Sprintf("%d:%d:%s", l.CharStart, l.CharEnd, l.Rule)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, l)
		}
	}
	return out, nil
}

// lastSpace is the index just after the last space, tab or newline in text[start:i],
// or start when there is none. The whitespace stays in the earlier chunk, so the
// next chunk begins on a word rather than with a leading space.
//
// A byte scan is safe here: every byte it matches is ASCII, and an ASCII byte can
// never be part of a multi-byte rune.
func lastSpace(text string, start, i int) int {
	for j := i - 1; j > start; j-- {
		switch text[j] {
		case ' ', '\t', '\n', '\r':
			return j + 1
		}
	}
	return start
}

// sentenceSegments splits text into byte ranges of at most chunkBytes, cutting
// after a sentence end where one is available. Every boundary lands on a rune
// start, so no multi-byte character is ever split. A sentence longer than a chunk
// is cut where it stands: an unbounded chunk is the failure this exists to stop.
func sentenceSegments(text string) [][2]int {
	var segs [][2]int
	start, cut := 0, -1
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' {
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\n') {
				j++
			}
			cut = j
		}
		if i+utf8.RuneLen(r)-start < chunkBytes {
			continue
		}
		// The rune at i no longer fits. Prefer the last sentence end, but never
		// past i — the cut has to stay inside the budget and on a rune boundary.
		end := cut
		if end <= start || end > i {
			// No sentence end inside this chunk — a bullet list, a table, a comma
			// run-on, or text whose sentence end is not '.!?'. Cut at the last
			// whitespace instead: cutting at i splits a word, and the engine then
			// reports both halves as misspellings ("over" at a 12 KB boundary came
			// back as 'o' and 'ver' on a text with nothing wrong with it).
			end = lastSpace(text, start, i)
			if end <= start {
				// One word longer than a chunk: nothing left to cut on. The match
				// that straddles this is dropped, so the worst case is two halves
				// of a word the engine cannot recognise anyway (a 12 KB garbage
				// blob), never a word from ordinary prose.
				end = i
			}
		}
		segs = append(segs, [2]int{start, end})
		start, cut = end, -1
	}
	if start < len(text) {
		segs = append(segs, [2]int{start, len(text)})
	}
	return segs
}
