package bwrap

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/accounts"
	"github.com/AgentDrasil/asgard/llms"
)

// Account selection strategy
//
// chooseAccount(cli, model) picks the account an agent run uses, per
// (cli, model), so that one account can serve a model until its quota is
// exhausted while a concurrent run needing another model that the account has
// no quota for uses a different account:
//
//  1. The ASGARD_<CLI>_ACCOUNT override wins (accounts.Select semantics).
//  2. With 0 or 1 accounts there is nothing to choose and no quota lookup.
//  3. Otherwise accounts are tried in sorted alias order and the first one
//     that still has quota for the model wins. A model the account reports no
//     entry for counts as available; an entry is exhausted when its Remaining
//     or any of its limits' Remaining is <= 0, or, when the aw query attached
//     a quota policy verdict (the account's bound profile), when that
//     verdict says it is not usable.
//  4. An account whose quota cannot be fetched is never preferred (its
//     pinned query fails on its own broken credentials): it is skipped with a
//     warning. If no account was confirmed available and at least one
//     errored, the first alias in sorted order is used.
//  5. Only when every account answered and all are exhausted, the account
//     whose quota refreshes first is picked.
//
// Per-account quota is cached for accountQuotaTTL, and concurrent lookups of
// the same account share one fetch. There is no optimistic accounting: the
// cache is only refreshed by TTL expiry.
const (
	accountQuotaTTL      = 60 * time.Second
	accountQuotaErrTTL   = 10 * time.Second
	accountQuotaFetchMax = 30 * time.Second
)

// quotaFetch returns the quota of one account of a CLI. It defaults to the
// pinned-account sandbox query; a variable so tests can stub it.
var quotaFetch func(ctx context.Context, cli, alias string) ([]llms.ModelUsage, error)

// Set in init: a direct initializer would be an initialization cycle
// (the query path reaches chooseAccount).
func init() { quotaFetch = QueryAccountQuota }

// listAccounts lists a CLI's account aliases; a variable so tests can stub it.
var listAccounts = accounts.List

// quotaNow is the clock of the quota cache; a variable for tests.
var quotaNow = time.Now

// QueryAccountQuota runs `aw usage --cli <cli>` in a query sandbox pinned to
// the given account and returns that CLI's usages, each tagged with alias.
func QueryAccountQuota(ctx context.Context, cli, alias string) ([]llms.ModelUsage, error) {
	out, err := RunQueryWithAccounts(ctx, map[string]string{cli: alias}, "usage", "--cli", cli)
	if err != nil {
		return nil, err
	}
	var res map[string][]llms.ModelUsage
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("parsing aw usage output: %w", err)
	}
	usages := res[cli]
	for i := range usages {
		usages[i].Account = alias
	}
	return usages, nil
}

// AllAccountUsages returns the cached quota of every account of cli for
// agentwrapper.CheckTarget. Accounts are fetched concurrently, and accounts
// that have not answered when ctx ends are left out (their fetch continues
// and fills the cache). ok is false when account selection is not in play (an
// account override, or fewer than two accounts) or no account answered.
func AllAccountUsages(ctx context.Context, cli string) ([]agentwrapper.AccountUsage, bool) {
	if os.Getenv(accountEnvName(cli)) != "" {
		return nil, false
	}
	aliases, err := listAccounts(cli)
	if err != nil || len(aliases) < 2 {
		return nil, false
	}

	type answer struct {
		idx    int
		usages []llms.ModelUsage
		err    error
	}
	answers := make(chan answer, len(aliases))
	for i, alias := range aliases {
		go func() {
			usages, err := cachedAccountQuota(cli, alias)
			answers <- answer{i, usages, err}
		}()
	}

	byIdx := make([]*agentwrapper.AccountUsage, len(aliases))
collect:
	for range aliases {
		select {
		case a := <-answers:
			if a.err != nil {
				log.Warn().Err(a.err).Str("cli", cli).Str("account", aliases[a.idx]).Msg("account quota unavailable, skipping it")
				continue
			}
			byIdx[a.idx] = &agentwrapper.AccountUsage{Account: aliases[a.idx], Usages: a.usages}
		case <-ctx.Done():
			log.Warn().Str("cli", cli).Msg("timed out waiting for account quota, using the accounts that answered")
			break collect
		}
	}

	var all []agentwrapper.AccountUsage
	for _, a := range byIdx {
		if a != nil {
			all = append(all, *a)
		}
	}
	return all, len(all) > 0
}

type quotaKey struct{ cli, alias string }

type quotaResult struct {
	usages []llms.ModelUsage
	err    error
}

type quotaCall struct {
	done chan struct{}
	quotaResult
}

type quotaEntry struct {
	quotaResult
	expires time.Time
}

var quotaCache = struct {
	mu       sync.Mutex
	entries  map[quotaKey]quotaEntry
	inflight map[quotaKey]*quotaCall
}{entries: map[quotaKey]quotaEntry{}, inflight: map[quotaKey]*quotaCall{}}

// cachedAccountQuota returns the account's quota from the cache, fetching it
// (once for all concurrent callers) when missing or expired.
func cachedAccountQuota(cli, alias string) ([]llms.ModelUsage, error) {
	k := quotaKey{cli, alias}
	quotaCache.mu.Lock()
	if e, ok := quotaCache.entries[k]; ok && quotaNow().Before(e.expires) {
		quotaCache.mu.Unlock()
		return e.usages, e.err
	}
	if c, ok := quotaCache.inflight[k]; ok {
		quotaCache.mu.Unlock()
		<-c.done
		return c.usages, c.err
	}
	c := &quotaCall{done: make(chan struct{})}
	quotaCache.inflight[k] = c
	quotaCache.mu.Unlock()

	// The fetch is shared, so it must not be bound to one caller's lifetime.
	ctx, cancel := context.WithTimeout(context.Background(), accountQuotaFetchMax)
	usages, err := quotaFetch(ctx, cli, alias)
	cancel()

	ttl := accountQuotaTTL
	if err != nil {
		ttl = accountQuotaErrTTL
	}
	quotaCache.mu.Lock()
	c.usages, c.err = usages, err
	quotaCache.entries[k] = quotaEntry{quotaResult: c.quotaResult, expires: quotaNow().Add(ttl)}
	delete(quotaCache.inflight, k)
	quotaCache.mu.Unlock()
	close(c.done)
	return usages, err
}

// stripProvider drops a leading "<provider>/" from a model name.
func stripProvider(m string) string {
	if i := strings.Index(m, "/"); i >= 0 {
		return m[i+1:]
	}
	return m
}

func modelMatches(want, have string) bool {
	return strings.EqualFold(want, have) || strings.EqualFold(stripProvider(want), stripProvider(have))
}

// usageExhausted reports whether the account should not serve the model. The
// quota policy verdict, when the aw query attached one, is authoritative: the
// account's profile decides per window. Otherwise only a fully used limit
// counts.
func usageExhausted(u llms.ModelUsage) bool {
	if u.Usable != nil {
		return !*u.Usable
	}
	if u.Remaining <= 0 {
		return true
	}
	for _, l := range u.Limits {
		if l.Remaining <= 0 {
			return true
		}
	}
	return false
}

// modelAvailability reports whether usages leave quota for model, and when an
// exhausted model refreshes (0 if unknown).
func modelAvailability(usages []llms.ModelUsage, model string) (available bool, refresh int64) {
	matched := false
	for _, u := range usages {
		if !modelMatches(model, u.Model) {
			continue
		}
		matched = true
		if !usageExhausted(u) {
			return true, 0
		}
		if u.RefreshDate > 0 && (refresh == 0 || u.RefreshDate < refresh) {
			refresh = u.RefreshDate
		}
	}
	return !matched, refresh
}

// chooseAccount is the single place where the agent run's account for
// (cli, model) is chosen; see the strategy comment above.
func chooseAccount(cli, model string) (string, bool, error) {
	first, ok, err := selectAccount(cli)
	if err != nil || !ok || os.Getenv(accountEnvName(cli)) != "" {
		return first, ok, err
	}
	aliases, err := listAccounts(cli)
	if err != nil {
		log.Warn().Err(err).Str("cli", cli).Msg("listing accounts failed, using the first account")
		return first, ok, nil
	}
	if len(aliases) < 2 {
		return first, ok, nil
	}

	bestAlias, bestRefresh := "", int64(math.MaxInt64)
	errored := false
	for _, alias := range aliases {
		usages, err := cachedAccountQuota(cli, alias)
		if err != nil {
			log.Warn().Err(err).Str("cli", cli).Str("account", alias).Msg("account quota unavailable, skipping it")
			errored = true
			continue
		}
		avail, refresh := modelAvailability(usages, model)
		if avail {
			log.Debug().Str("cli", cli).Str("model", model).Str("account", alias).Msg("selected account with remaining quota")
			return alias, true, nil
		}
		if refresh == 0 {
			refresh = math.MaxInt64
		}
		if bestAlias == "" || refresh < bestRefresh {
			bestAlias, bestRefresh = alias, refresh
		}
	}
	if errored {
		log.Warn().Str("cli", cli).Str("model", model).Str("account", aliases[0]).Msg("no account confirmed available and some quota unknown, using the first account")
		return aliases[0], true, nil
	}
	log.Warn().Str("cli", cli).Str("model", model).Str("account", bestAlias).Msg("all accounts exhausted for model, picking the earliest refresh")
	return bestAlias, true, nil
}

// accountEnvName is the env var that overrides account selection for a CLI.
func accountEnvName(cli string) string {
	return "ASGARD_" + strings.ToUpper(cli) + "_ACCOUNT"
}
