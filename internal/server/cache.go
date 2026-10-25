package server

import (
	"os"
	"strings"
	"sync"
)

// maxCachedTables bounds how many parsed files are held in memory at once.
const maxCachedTables = 6

// tableCache keeps parsed files around so that scrolling, sorting and
// filtering do not re-read the file every time. An entry is dropped as soon as
// the file on disk changes, which is what makes live reload cheap.
type tableCache struct {
	mu    sync.Mutex
	items map[string]*Table
	lru   []string
}

func newTableCache() *tableCache {
	return &tableCache{items: map[string]*Table{}}
}

func cacheKey(path string, opts ParseOptions) string {
	return strings.ToLower(path) + "\x00" + opts.key()
}

// get returns a parsed table, reusing the cached copy when the file has not
// changed underneath it.
func (c *tableCache) get(path string, opts ParseOptions) (*Table, error) {
	key := cacheKey(path, opts)

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if t, ok := c.items[key]; ok {
		if t.ModTime.Equal(info.ModTime()) && t.Size == info.Size() {
			c.touch(key)
			c.mu.Unlock()
			return t, nil
		}
		delete(c.items, key)
		c.drop(key)
	}
	c.mu.Unlock()

	t, err := LoadTable(path, opts)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.items[key] = t
	c.touch(key)
	for len(c.lru) > maxCachedTables {
		oldest := c.lru[0]
		c.lru = c.lru[1:]
		delete(c.items, oldest)
	}
	c.mu.Unlock()
	return t, nil
}

// invalidate forgets every parse of one file, whatever options were used.
func (c *tableCache) invalidate(path string) {
	prefix := strings.ToLower(path) + "\x00"
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		if strings.HasPrefix(k, prefix) {
			delete(c.items, k)
			c.drop(k)
		}
	}
}

// touch records a key as most recently used. Caller holds the lock.
func (c *tableCache) touch(key string) {
	c.drop(key)
	c.lru = append(c.lru, key)
}

// drop removes a key from the recency list. Caller holds the lock.
func (c *tableCache) drop(key string) {
	for i, k := range c.lru {
		if k == key {
			c.lru = append(c.lru[:i], c.lru[i+1:]...)
			return
		}
	}
}
