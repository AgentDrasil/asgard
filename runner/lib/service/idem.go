package service

import (
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultIdemTTL = 10 * time.Minute
	defaultIdemMax = 1024
)

// idemCache makes mutating RPCs idempotent per request_id: a retry attaches to
// the in-flight call or gets the cached result of the completed one. Entries
// expire after ttl and the cache is bounded by max (oldest evicted first).
type idemCache struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*idemEntry
	order   []string
}

type idemEntry struct {
	done   chan struct{}
	val    any
	err    error
	expiry time.Time
}

func newIdemCache(ttl time.Duration, max int) *idemCache {
	if ttl <= 0 {
		ttl = defaultIdemTTL
	}
	if max <= 0 {
		max = defaultIdemMax
	}
	return &idemCache{ttl: ttl, max: max, now: time.Now, entries: make(map[string]*idemEntry)}
}

// do runs fn once per (method, id); an empty id always runs fn.
func (c *idemCache) do(method, id string, fn func() (any, error)) (any, error) {
	if id == "" {
		return fn()
	}
	key := method + "/" + id
	c.mu.Lock()
	c.expireLocked()
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		<-e.done
		return e.val, e.err
	}
	e := &idemEntry{done: make(chan struct{})}
	c.entries[key] = e
	c.order = append(c.order, key)
	c.evictLocked()
	c.mu.Unlock()

	func() {
		defer func() {
			if r := recover(); r != nil {
				e.val, e.err = nil, status.Errorf(codes.Internal, "panic in %s: %v", method, r)
			}
		}()
		e.val, e.err = fn()
	}()
	c.mu.Lock()
	e.expiry = c.now().Add(c.ttl)
	c.evictLocked()
	c.mu.Unlock()
	close(e.done)
	return e.val, e.err
}

// evictLocked drops the oldest completed entries while over the bound.
// In-flight entries are never evicted, so a retry can always attach.
func (c *idemCache) evictLocked() {
	for len(c.order) > c.max {
		idx := -1
		for i, k := range c.order {
			if e := c.entries[k]; e != nil && !e.expiry.IsZero() {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		delete(c.entries, c.order[idx])
		c.order = append(c.order[:idx], c.order[idx+1:]...)
	}
}

func (c *idemCache) expireLocked() {
	now := c.now()
	n := 0
	for _, k := range c.order {
		e := c.entries[k]
		if e != nil && !e.expiry.IsZero() && now.After(e.expiry) {
			delete(c.entries, k)
			continue
		}
		c.order[n] = k
		n++
	}
	c.order = c.order[:n]
}
