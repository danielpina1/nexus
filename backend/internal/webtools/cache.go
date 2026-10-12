package webtools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// cachedPage is a fetched response stored on disk.
type cachedPage struct {
	URL         string    `json:"url"`
	FinalURL    string    `json:"final_url"`
	Status      int       `json:"status"`
	ContentType string    `json:"content_type"`
	Truncated   bool      `json:"truncated"` // body hit the download cap
	FetchedAt   time.Time `json:"fetched_at"`
	Body        []byte    `json:"-"`
}

// diskCache stores pages as <key>.json (metadata) + <key>.body.
type diskCache struct {
	dir      string
	ttl      time.Duration
	maxBytes int64
	now      func() time.Time
	mu       sync.Mutex
}

func newDiskCache(dir string, ttl time.Duration, maxBytes int64) (*diskCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &diskCache{dir: dir, ttl: ttl, maxBytes: maxBytes, now: time.Now}, nil
}

func cacheKey(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:16])
}

func (c *diskCache) paths(u string) (meta, body string) {
	k := cacheKey(u)
	sub := filepath.Join(c.dir, k[:2])
	return filepath.Join(sub, k+".json"), filepath.Join(sub, k+".body")
}

// Get returns a fresh cached page for u.
func (c *diskCache) Get(u string) (*cachedPage, bool) {
	if c == nil {
		return nil, false
	}
	mp, bp := c.paths(u)
	c.mu.Lock()
	defer c.mu.Unlock()
	mb, err := os.ReadFile(mp)
	if err != nil {
		return nil, false
	}
	var p cachedPage
	if json.Unmarshal(mb, &p) != nil || p.URL != u {
		return nil, false
	}
	if c.now().Sub(p.FetchedAt) > c.ttl {
		_ = os.Remove(mp)
		_ = os.Remove(bp)
		return nil, false
	}
	b, err := os.ReadFile(bp)
	if err != nil {
		return nil, false
	}
	p.Body = b
	return &p, true
}

// Put stores p (keyed by p.URL).
func (c *diskCache) Put(p *cachedPage) error {
	if c == nil {
		return nil
	}
	mp, bp := c.paths(p.URL)
	mb, err := json.Marshal(p)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(mp), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(bp, p.Body); err != nil {
		return err
	}
	return writeAtomic(mp, mb)
}

func writeAtomic(p string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Prune removes expired entries, then the oldest ones while the cache is
// larger than maxBytes. It returns the number of entries removed.
func (c *diskCache) Prune() (int, error) {
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	type entry struct {
		meta, body string
		mod        time.Time
		size       int64
	}
	var entries []entry
	var total int64
	err := filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".json") {
			if !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
				_ = os.Remove(p)
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		e := entry{meta: p, body: strings.TrimSuffix(p, ".json") + ".body", mod: fi.ModTime(), size: fi.Size()}
		if bi, err := os.Stat(e.body); err == nil {
			e.size += bi.Size()
		}
		entries = append(entries, e)
		total += e.size
		return nil
	})
	if err != nil {
		return 0, err
	}
	removed := 0
	rm := func(e entry) {
		_ = os.Remove(e.meta)
		_ = os.Remove(e.body)
		total -= e.size
		removed++
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
	now := c.now()
	var keep []entry
	for _, e := range entries {
		if now.Sub(e.mod) > c.ttl {
			rm(e)
		} else {
			keep = append(keep, e)
		}
	}
	for i := 0; total > c.maxBytes && i < len(keep); i++ {
		rm(keep[i])
	}
	return removed, nil
}
