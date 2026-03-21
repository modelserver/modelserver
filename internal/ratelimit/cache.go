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

// NewCreditCache creates a new credit cache with the given TTL.
func NewCreditCache(ttl time.Duration) *creditCache {
	return &creditCache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
	}
}

// newCreditCache is kept for internal use as an alias.
func newCreditCache(ttl time.Duration) *creditCache {
	return NewCreditCache(ttl)
}

// Get retrieves a cached value.
func (c *creditCache) Get(key string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return 0, false
	}
	return entry.value, true
}

// get is an alias for Get for internal use.
func (c *creditCache) get(key string) (float64, bool) {
	return c.Get(key)
}

// Set stores a value in the cache.
func (c *creditCache) Set(key string, value float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// set is an alias for Set for internal use.
func (c *creditCache) set(key string, value float64) {
	c.Set(key, value)
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
