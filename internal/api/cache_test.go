package api

import (
	"testing"

	"grammar-server/internal/engine"
)

func TestLintCacheHitsAndMisses(t *testing.T) {
	c := newLintCache(4)
	k := lintKey("fp", "same text")
	if _, ok := c.get(k); ok {
		t.Fatal("an empty cache reported a hit")
	}
	c.put(k, []engine.Lint{{Rule: "SpellCheck", CharStart: 3, CharEnd: 6}})
	got, ok := c.get(k)
	if !ok || len(got) != 1 || got[0].Rule != "SpellCheck" || got[0].CharStart != 3 {
		t.Fatalf("get after put = %+v, %v", got, ok)
	}
	if _, hits, misses := c.stats(); hits != 1 || misses != 1 {
		t.Fatalf("stats = %d hits, %d misses, want 1 and 1", hits, misses)
	}
}

// The key must cover the configuration as well as the text. Rules and dialect are
// engine state rather than per-request filters, so the same words lint differently
// under a different rule set — a text-only key would serve the old answer as if it
// were the new one, which is the one way a cache like this can be badly wrong.
func TestLintKeySeparatesConfigurations(t *testing.T) {
	a := lintKey("fp-american-default", "The colour is fine.")
	b := lintKey("fp-british-picky", "The colour is fine.")
	if a == b {
		t.Fatal("two configurations produced the same key")
	}
	c := newLintCache(4)
	c.put(a, []engine.Lint{{Rule: "A"}})
	if _, ok := c.get(b); ok {
		t.Fatal("a check under one configuration hit another configuration's entry")
	}
}

// Two fields hashed together need a separator, or moving text across the boundary
// produces the same key.
func TestLintKeySeparatesFingerprintFromText(t *testing.T) {
	if lintKey("ab", "c") == lintKey("a", "bc") {
		t.Fatal("the fingerprint and the text are not separated")
	}
}

func TestLintCacheEvictsTheOldest(t *testing.T) {
	c := newLintCache(2)
	c.put(lintKey("f", "one"), []engine.Lint{{Rule: "1"}})
	c.put(lintKey("f", "two"), []engine.Lint{{Rule: "2"}})
	c.put(lintKey("f", "three"), []engine.Lint{{Rule: "3"}})
	if entries, _, _ := c.stats(); entries != 2 {
		t.Fatalf("entries = %d, want the cap of 2", entries)
	}
	if _, ok := c.get(lintKey("f", "one")); ok {
		t.Error("the oldest entry survived eviction")
	}
	if _, ok := c.get(lintKey("f", "three")); !ok {
		t.Error("the newest entry was evicted instead")
	}
}

// A cache is only trustworthy if a caller cannot corrupt it: checkChunked shifts
// offsets as it collects results, so a shared slice would let one request poison
// every later read. get() hands out a copy, and this pins that.
func TestLintCacheCannotBePoisonedByACaller(t *testing.T) {
	c := newLintCache(2)
	k := lintKey("f", "teh cat")
	c.put(k, []engine.Lint{{Rule: "SpellCheck", CharStart: 0, CharEnd: 3}})

	got, _ := c.get(k)
	for i := range got { // exactly what a careless caller would do
		got[i].CharStart += 1000
		got[i].CharEnd += 1000
	}

	again, _ := c.get(k)
	if again[0].CharStart != 0 || again[0].CharEnd != 3 {
		t.Fatalf("the cached entry was mutated by a caller: %+v", again[0])
	}
}

// The cache is read and written from concurrent requests, so the lock has to hold
// under -race, not just in principle.
func TestLintCacheIsConcurrencySafe(t *testing.T) {
	c := newLintCache(16)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				k := lintKey("fp", string(rune('a'+i))+string(rune('0'+j%10)))
				if _, ok := c.get(k); !ok {
					c.put(k, []engine.Lint{{Rule: "X"}})
				}
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if entries, hits, misses := c.stats(); entries > 16 || hits+misses == 0 {
		t.Fatalf("stats look wrong: %d entries, %d hits, %d misses", entries, hits, misses)
	}
}
