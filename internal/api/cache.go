package api

import (
	"container/list"
	"crypto/sha256"
	"slices"
	"sync"

	"grammar-server/internal/engine"
)

// defaultCacheEntries is the entry cap. A chunk is at most 1.5 KB plus a handful
// of lints, so this is a couple of MB at most — bounded on purpose, because an
// unbounded cache keyed on user text is a memory leak with a nice name.
const defaultCacheEntries = 512

// lintCache remembers the engine's verdict on one chunk of text.
//
// It exists because a client re-sends almost the same document on every pause:
// the typing watcher sends a caret window that overlaps its predecessor, and a
// client that re-checks a whole document after each keystroke does the same. Without
// this, every one of those re-checks rechecks every chunk, so a 50 KB document costs
// 8.8 s again and again for text that has not changed.
//
// The key is the chunk AND the configuration it was checked under, because the
// rules and the dialect are engine state rather than per-request filters: text
// alone would let a lint produced under one rule set be served under another.
//
// ponytail: one lock, fixed entry cap. Shard it or bound it by bytes if
// profiling ever shows contention; the entries are small and the cap is the
// ceiling that matters more than the lock.
type lintCache struct {
	mu     sync.Mutex
	cap    int
	order  *list.List // most recently used at the front
	items  map[[32]byte]*list.Element
	hits   int64
	misses int64
}

type cacheEntry struct {
	key   [32]byte
	lints []engine.Lint
}

func newLintCache(capacity int) *lintCache {
	if capacity <= 0 {
		capacity = defaultCacheEntries
	}
	return &lintCache{cap: capacity, order: list.New(), items: map[[32]byte]*list.Element{}}
}

// get returns the lints cached for a chunk, as a copy. Callers shift offsets as
// they collect results, and one caller mutating the slice in place would poison
// the entry for every later reader; a clone costs one small allocation against a
// ~150 ms engine call, and makes the entry unforgeable from outside.
func (c *lintCache) get(key [32]byte) ([]engine.Lint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	c.order.MoveToFront(el)
	c.hits++
	return slices.Clone(el.Value.(*cacheEntry).lints), true
}

func (c *lintCache) put(key [32]byte, lints []engine.Lint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).lints = lints
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&cacheEntry{key: key, lints: lints})
	for c.order.Len() > c.cap {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		delete(c.items, back.Value.(*cacheEntry).key)
	}
}

// stats reports what the cache has done, for /status. A cache whose hits nobody
// can see is indistinguishable from a cache that never works.
func (c *lintCache) stats() (entries int, hits, misses int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len(), c.hits, c.misses
}

// lintKey is the identity of one engine call: the configuration it ran under and
// the exact text it was given. A NUL separates the two so no combination of a
// fingerprint and a chunk can collide with a different pair.
func lintKey(fingerprint, chunk string) [32]byte {
	h := sha256.New()
	h.Write([]byte(fingerprint))
	h.Write([]byte{0})
	h.Write([]byte(chunk))
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}
