package api

import (
	"context"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/llms"
)

const (
	quotaCacheTTL      = 60 * time.Second
	quotaCacheStaleTTL = 5 * time.Minute
	quotaCacheErrTTL   = 10 * time.Second
)

type quotaCacheState int

const (
	quotaCacheMiss quotaCacheState = iota
	quotaCacheHit
	quotaCacheStale
)

func (s quotaCacheState) headerValue() string {
	switch s {
	case quotaCacheHit:
		return "hit"
	case quotaCacheStale:
		return "stale"
	default:
		return "miss"
	}
}

type quotaCacheEntry struct {
	data      map[string][]llms.ModelUsage
	err       error
	fetchedAt time.Time
}

type quotaInflight struct {
	done chan struct{}
	data map[string][]llms.ModelUsage
	err  error
}

// quotaCache provides per-runner TTL caching, singleflight deduplication, and
// stale-while-revalidate background refreshes for GET /api/quota.
// Its zero value is ready to use.
type quotaCache struct {
	mu       sync.Mutex
	entries  map[string]quotaCacheEntry
	inflight map[string]*quotaInflight
	now      func() time.Time
}

func (c *quotaCache) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *quotaCache) get(
	ctx context.Context,
	node string,
	forceRefresh bool,
	fetch func(context.Context) (map[string][]llms.ModelUsage, error),
) (map[string][]llms.ModelUsage, quotaCacheState, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]quotaCacheEntry)
		c.inflight = make(map[string]*quotaInflight)
	}
	now := c.currentTime()

	if !forceRefresh {
		if e, ok := c.entries[node]; ok {
			age := now.Sub(e.fetchedAt)
			if e.err != nil {
				if age < quotaCacheErrTTL {
					c.mu.Unlock()
					return nil, quotaCacheHit, e.err
				}
			} else if age < quotaCacheTTL {
				c.mu.Unlock()
				return cloneQuotaMap(e.data), quotaCacheHit, nil
			} else if age < quotaCacheStaleTTL {
				if _, running := c.inflight[node]; !running {
					call := &quotaInflight{done: make(chan struct{})}
					c.inflight[node] = call
					go c.runFetch(node, call, fetch)
				}
				c.mu.Unlock()
				return cloneQuotaMap(e.data), quotaCacheStale, nil
			}
		}
	}

	call, running := c.inflight[node]
	if !running {
		call = &quotaInflight{done: make(chan struct{})}
		c.inflight[node] = call
		go c.runFetch(node, call, fetch)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return cloneQuotaMap(call.data), quotaCacheMiss, call.err
	case <-ctx.Done():
		return nil, quotaCacheMiss, ctx.Err()
	}
}

func (c *quotaCache) runFetch(
	node string,
	call *quotaInflight,
	fetch func(context.Context) (map[string][]llms.ModelUsage, error),
) {
	// Shared fetch is detached from any single HTTP request so one client
	// disconnecting does not abort the refresh for other callers.
	ctx, cancel := context.WithTimeout(context.Background(), quotaTimeout)
	data, err := fetch(ctx)
	cancel()

	c.mu.Lock()
	call.data, call.err = data, err
	c.entries[node] = quotaCacheEntry{
		data:      cloneQuotaMap(data),
		err:       err,
		fetchedAt: c.currentTime(),
	}
	delete(c.inflight, node)
	c.mu.Unlock()
	close(call.done)
}

func cloneQuotaMap(src map[string][]llms.ModelUsage) map[string][]llms.ModelUsage {
	if src == nil {
		return nil
	}
	dst := make(map[string][]llms.ModelUsage, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		cp := make([]llms.ModelUsage, len(v))
		for i, u := range v {
			if len(u.Limits) > 0 {
				lims := make([]llms.QuotaLimit, len(u.Limits))
				copy(lims, u.Limits)
				u.Limits = lims
			}
			if u.Balance != nil {
				b := *u.Balance
				if len(u.Balance.BalanceInfos) > 0 {
					infos := make([]llms.BalanceInfo, len(u.Balance.BalanceInfos))
					copy(infos, u.Balance.BalanceInfos)
					b.BalanceInfos = infos
				}
				u.Balance = &b
			}
			cp[i] = u
		}
		dst[k] = cp
	}
	return dst
}
