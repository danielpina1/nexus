package vault

import "sync"

// recentCache keeps the content of recently written notes by content hash,
// so a stale write can be three-way merged against its base even when the
// base was never committed (batched git commits, no-git vaults).
type recentCache struct {
	mu       sync.Mutex
	maxItems int
	maxBytes int
	bytes    int
	order    []string
	items    map[string][]byte
}

func newRecentCache(maxItems, maxBytes int) *recentCache {
	return &recentCache{maxItems: maxItems, maxBytes: maxBytes, items: map[string][]byte{}}
}

func (c *recentCache) put(hash string, b []byte) {
	if len(b) > c.maxBytes/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[hash]; ok {
		return
	}
	c.items[hash] = append([]byte(nil), b...)
	c.order = append(c.order, hash)
	c.bytes += len(b)
	for len(c.order) > c.maxItems || c.bytes > c.maxBytes {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= len(c.items[old])
		delete(c.items, old)
	}
}

func (c *recentCache) get(hash string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[hash]
	return b, ok
}
