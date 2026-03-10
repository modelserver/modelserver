package ratelimit

import (
	"sync"
	"time"
)

type creditCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

type cacheEntry struct {
	value     float64
	expiresAt time.Time
}

func newCreditCache(ttl time.Duration) *creditCache {
	return &creditCache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
	}
}

func (c *creditCache) get(key string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return 0, false
	}
	return entry.value, true
}

func (c *creditCache) set(key string, value float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *creditCache) invalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(c.entries, key)
		}
	}
}
