// Package cache is the in-process TTL cache fronting GetEffective (the hot path).
// Entries expire after a fixed TTL and are evicted en-masse per owner on a
// SetOverride. It is safe for concurrent use.
//
// Other services do NOT share this cache; they keep their own and invalidate it by
// subscribing to restorna.settings.override.changed.v1 (see README).
package cache

import (
	"strings"
	"sync"
	"time"

	"github.com/restorna/platform/services/settings/internal/domain"
)

// entry is a cached value with its expiry.
type entry struct {
	val     domain.SettingValue
	expires time.Time
}

// TTL is the in-process cache for effective values. The cache key is
// "owner|brand|restaurant|key"; InvalidateOwner drops every entry whose key has
// the owner prefix.
type TTL struct {
	mu  sync.RWMutex
	ttl time.Duration
	now func() time.Time
	m   map[string]entry
}

// New builds a cache with the given TTL. A non-positive ttl disables expiry-based
// eviction (entries live until invalidated).
func New(ttl time.Duration) *TTL {
	return &TTL{ttl: ttl, now: time.Now, m: map[string]entry{}}
}

// Get returns the value for a live (un-expired) entry.
func (c *TTL) Get(key string) (domain.SettingValue, bool) {
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if !ok {
		return domain.SettingValue{}, false
	}
	if c.ttl > 0 && c.now().After(e.expires) {
		c.mu.Lock()
		delete(c.m, key)
		c.mu.Unlock()
		return domain.SettingValue{}, false
	}
	return e.val, true
}

// Set stores a value with the configured TTL.
func (c *TTL) Set(key string, v domain.SettingValue) {
	c.mu.Lock()
	c.m[key] = entry{val: v, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
}

// InvalidateOwner removes every entry belonging to an owner. Cache keys are
// "owner|brand|restaurant|key", so an owner's entries share the "owner|" prefix.
func (c *TTL) InvalidateOwner(ownerID string) {
	prefix := ownerID + "|"
	c.mu.Lock()
	for k := range c.m {
		if strings.HasPrefix(k, prefix) {
			delete(c.m, k)
		}
	}
	c.mu.Unlock()
}
