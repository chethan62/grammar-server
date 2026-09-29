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
// Measured on this box (CPU-only, 85-95C package temp, so roughly 2x pessimistic):
//
//	40 chars      11.7 ms p50
//	200 chars     15.6 ms p50
//	1 000 chars  113.0 ms p50
//	10 000 chars  10 037 ms  <- the diagnostics deadline, not harper's real cost
//
// The 10 KB figure is the deadline, and that is the bug this constant was set
// wrong for: at 12 KB per chunk a 10 KB text went to the engine as ONE call,
// missed the 10 s deadline, and came back with partial results — while a 200 KB
// document became 17 such calls, so one request held the engine for ~170 s and
// starved everything else, /status included (measured: GET /status 6.5 s, and
// /v2/check at 10.13s, 9.98s, 10.04s).
//
// 1 500 bytes keeps one call at roughly 150-200 ms, far under the deadline, so
// no chunk can time out at all. The total for a document is then character-bound
// and linear, about 0.1 ms per character: 10 KB ~1 s, 200 KB ~20 s, and the
// interactive path (~200 characters) ~16 ms.
//
// ponytail: sequential chunks. Pipeline them only if a character-bound 20 s
// worst case ever matters; the typing watcher never sends more than ~500 bytes.
const chunkBytes = 1_500

// checkChunked lints text of any length by splitting it at sentence boundaries
// and shifting each chunk's offsets back to the whole document.
//
// Splitting only at sentence ends means no word is ever cut in half, and a match
// that still straddles a boundary is dropped rather than reported at an offset
// that points at the wrong characters.
func (s *Server) checkChunked(text string) ([]engine.Lint, error) {
	// Read once per request, not per chunk: it is engine state and cannot change
	// while this call is running.
	fp := s.eng.Fingerprint()
	if len(text) <= chunkBytes {
		// One chunk is the whole document, so its offsets are already absolute.
		return s.checkCached(fp, text)
	}
	var out []engine.Lint
	seen := map[string]bool{}
	for _, seg := range sentenceSegments(text) {
		lints, err := s.checkCached(fp, text[seg[0]:seg[1]])
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

// checkCached is the engine's verdict on one chunk, from the cache when that
// exact chunk was already checked under that exact configuration. A hit and a
// miss return the same value, because only the shift-and-collect loop above ever
// post-processes a chunk's lints — the cache sits underneath that loop, never
// beside it.
func (s *Server) checkCached(fp, chunk string) ([]engine.Lint, error) {
	key := lintKey(fp, chunk)
	if lints, ok := s.cache.get(key); ok {
		return lints, nil
	}
	lints, err := s.eng.Check(chunk)
	if err != nil {
		return nil, err
	}
	s.cache.put(key, lints)
	return lints, nil
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
