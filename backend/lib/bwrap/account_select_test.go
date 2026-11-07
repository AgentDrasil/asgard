package bwrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/llms"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// Never run a real sandboxed quota query from tests; selector tests install
// their own stub.
func init() {
	quotaFetch = func(context.Context, string, string) ([]llms.ModelUsage, error) {
		return nil, errors.New("quotaFetch not stubbed")
	}
}

type fakeQuota struct {
	mu    sync.Mutex
	data  map[string][]llms.ModelUsage // by alias
	errs  map[string]error
	calls map[string]int
	delay time.Duration
}

func (f *fakeQuota) fetch(_ context.Context, _, alias string) ([]llms.ModelUsage, error) {
	f.mu.Lock()
	f.calls[alias]++
	d, err := f.data[alias], f.errs[alias]
	f.mu.Unlock()
	time.Sleep(f.delay)
	return d, err
}

func (f *fakeQuota) count(alias string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[alias]
}

// setupSelector installs a stubbed quota fetcher and account list, with a
// fresh cache and a controllable clock.
func setupSelector(t *testing.T, aliases []string, f *fakeQuota) *atomic.Int64 {
	t.Helper()
	f.calls = map[string]int{}
	t.Setenv("ASGARD_CLAUDE_ACCOUNT", "")
	oldFetch, oldList, oldSel, oldNow := quotaFetch, listAccounts, selectAccount, quotaNow
	var now atomic.Int64
	now.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	quotaFetch = f.fetch
	listAccounts = func(string) ([]string, error) { return aliases, nil }
	selectAccount = func(string) (string, bool, error) {
		if len(aliases) == 0 {
			return "", false, nil
		}
		return aliases[0], true, nil
	}
	quotaNow = func() time.Time { return time.Unix(0, now.Load()) }
	quotaCache.mu.Lock()
	quotaCache.entries = map[quotaKey]quotaEntry{}
	quotaCache.mu.Unlock()
	t.Cleanup(func() {
		quotaFetch, listAccounts, selectAccount, quotaNow = oldFetch, oldList, oldSel, oldNow
		quotaCache.mu.Lock()
		quotaCache.entries = map[quotaKey]quotaEntry{}
		quotaCache.mu.Unlock()
	})
	return &now
}

func TestChooseAccount_PerModelQuota(t *testing.T) {
	f := &fakeQuota{data: map[string][]llms.ModelUsage{
		"a": {{Model: "gemini/gemini-3", Remaining: 0.5}, {Model: "gemini/gemini-4", Remaining: 0}},
		"b": {{Model: "gemini/gemini-4", Remaining: 0.8}},
	}}
	setupSelector(t, []string{"a", "b"}, f)

	alias, ok, err := chooseAccount("claude", "gemini-3")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "a", alias)

	alias, _, err = chooseAccount("claude", "gemini-4")
	require.NoError(t, err)
	assert.Equal(t, "b", alias)

	// Unknown model on every account counts as available: first alias.
	alias, _, _ = chooseAccount("claude", "other")
	assert.Equal(t, "a", alias)

	// Case-insensitive and provider-prefix matching.
	alias, _, _ = chooseAccount("claude", "GEMINI-4")
	assert.Equal(t, "b", alias)
	alias, _, _ = chooseAccount("claude", "google/gemini-4")
	assert.Equal(t, "b", alias)
}

func TestChooseAccount_ExhaustedLimit(t *testing.T) {
	f := &fakeQuota{data: map[string][]llms.ModelUsage{
		"a": {{Model: "m", Remaining: 0.9, Limits: []llms.QuotaLimit{{Name: "5h", Remaining: 0}}}},
		"b": {{Model: "m", Remaining: 0.9}},
	}}
	setupSelector(t, []string{"a", "b"}, f)
	alias, _, _ := chooseAccount("claude", "m")
	assert.Equal(t, "b", alias)
}

func TestChooseAccount_AllExhaustedPicksEarliestRefresh(t *testing.T) {
	f := &fakeQuota{data: map[string][]llms.ModelUsage{
		"a": {{Model: "m", Remaining: 0, RefreshDate: 300}},
		"b": {{Model: "m", Remaining: 0, RefreshDate: 100}},
		"c": {{Model: "m", Remaining: 0, RefreshDate: 200}},
	}}
	setupSelector(t, []string{"a", "b", "c"}, f)
	alias, ok, err := chooseAccount("claude", "m")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", alias)
}

func TestChooseAccount_FetchErrors(t *testing.T) {
	boom := errors.New("boom")
	ok1 := []llms.ModelUsage{{Model: "m", Remaining: 1}}
	none := []llms.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 5}}
	cases := []struct {
		name    string
		aliases []string
		data    map[string][]llms.ModelUsage
		errs    map[string]error
		want    string
	}{
		{"A errors, B has quota", []string{"a", "b"}, map[string][]llms.ModelUsage{"b": ok1}, map[string]error{"a": boom}, "b"},
		{"A errors, B exhausted", []string{"a", "b"}, map[string][]llms.ModelUsage{"b": none}, map[string]error{"a": boom}, "a"},
		{"B errors, A exhausted", []string{"a", "b"}, map[string][]llms.ModelUsage{"a": none}, map[string]error{"b": boom}, "a"},
		{"all error", []string{"a", "b"}, nil, map[string]error{"a": boom, "b": boom}, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupSelector(t, c.aliases, &fakeQuota{data: c.data, errs: c.errs})
			alias, ok, err := chooseAccount("claude", "m")
			require.NoError(t, err)
			assert.True(t, ok)
			assert.Equal(t, c.want, alias)
		})
	}
}

func TestChooseAccount_ListErrorDegradesToFirst(t *testing.T) {
	setupSelector(t, []string{"a", "b"}, &fakeQuota{})
	listAccounts = func(string) ([]string, error) { return nil, errors.New("boom") }
	alias, ok, err := chooseAccount("claude", "m")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "a", alias)
}

func TestChooseAccount_SingleAndNoAccounts(t *testing.T) {
	f := &fakeQuota{}
	setupSelector(t, []string{"only"}, f)
	alias, ok, err := chooseAccount("claude", "m")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "only", alias)
	assert.Zero(t, f.count("only"), "single account needs no quota lookup")

	setupSelector(t, nil, f)
	_, ok, err = chooseAccount("claude", "m")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestChooseAccount_EnvOverrideWins(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ASGARD_ACCOUNTS_DIR", root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "claude", "b"), 0o700))
	f := &fakeQuota{data: map[string][]llms.ModelUsage{"a": {{Model: "m", Remaining: 1}}}}
	setupSelector(t, []string{"a", "b"}, f)
	selectAccount = func(cli string) (string, bool, error) { return "b", true, nil }
	t.Setenv("ASGARD_CLAUDE_ACCOUNT", "b")
	alias, ok, err := chooseAccount("claude", "m")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", alias)
	assert.Zero(t, f.count("a")+f.count("b"))
}

func TestChooseAccount_CacheTTLAndSingleflight(t *testing.T) {
	f := &fakeQuota{
		data:  map[string][]llms.ModelUsage{"a": {{Model: "m", Remaining: 1}}, "b": {}},
		delay: 50 * time.Millisecond,
	}
	now := setupSelector(t, []string{"a", "b"}, f)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			alias, _, err := chooseAccount("claude", "m")
			assert.NoError(t, err)
			assert.Equal(t, "a", alias)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, f.count("a"), "concurrent calls share one fetch")

	// Within the TTL: cached.
	now.Add(int64(accountQuotaTTL - time.Second))
	_, _, _ = chooseAccount("claude", "m")
	assert.Equal(t, 1, f.count("a"))

	// After the TTL: refetched, picking up changed quota.
	f.mu.Lock()
	f.data["a"] = []llms.ModelUsage{{Model: "m", Remaining: 0}}
	f.data["b"] = []llms.ModelUsage{{Model: "m", Remaining: 1}}
	f.mu.Unlock()
	now.Add(int64(2 * time.Second))
	alias, _, _ := chooseAccount("claude", "m")
	assert.Equal(t, 2, f.count("a"))
	assert.Equal(t, "b", alias)
}

func TestAccountForSandbox_Pinned(t *testing.T) {
	f := &fakeQuota{}
	setupSelector(t, []string{"a", "b"}, f)
	selectAccount = func(string) (string, bool, error) { return "", false, errors.New("must not be consulted") }
	sel, err := accountForSandbox("claude", "m", true, map[string]string{"claude": "b"})
	require.NoError(t, err)
	assert.Equal(t, accountSel{alias: "b", ok: true, strict: true}, sel)
	// A CLI that is not pinned binds nothing.
	sel, err = accountForSandbox("agy", "m", true, map[string]string{"claude": "b"})
	require.NoError(t, err)
	assert.False(t, sel.ok)
	assert.Zero(t, f.count("a")+f.count("b"))
}

func TestBuildSandboxArgs_PinnedQueryAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accRoot := t.TempDir()
	t.Setenv("ASGARD_ACCOUNTS_DIR", accRoot)
	t.Setenv("ASGARD_AGY_ACCOUNT", "")
	runDir := filepath.Join(home, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini"), 0o755))
	tok := map[string]string{}
	for _, alias := range []string{"a", "b"} {
		d := filepath.Join(accRoot, "agy", alias)
		require.NoError(t, os.MkdirAll(d, 0o700))
		tok[alias] = filepath.Join(d, "antigravity-oauth-token")
		require.NoError(t, os.WriteFile(tok[alias], []byte("{}"), 0o600))
	}
	dest := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")

	// Pinned: exactly the pinned account, regardless of order.
	args, err := buildSandboxArgsPinned(map[string]string{"agy": "b"}, nil, "", agentspec.CLITarget{}, true, "", runDir, "", "q1", "", "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(args, "--bind", tok["b"], dest), 0)
	assert.Equal(t, -1, indexOfSeq(args, "--bind", tok["a"], dest))

	// Pinned to a missing account is an error, never a silent default.
	_, err = buildSandboxArgsPinned(map[string]string{"agy": "ghost"}, nil, "", agentspec.CLITarget{}, true, "", runDir, "", "q2", "", "")
	assert.Error(t, err)

	// Unpinned query sandbox keeps the legacy first-alias behaviour.
	args, err = buildSandboxArgsPinned(nil, nil, "", agentspec.CLITarget{}, true, "", runDir, "", "q3", "", "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(args, "--bind", tok["a"], dest), 0)
}

func TestChooseAccount_PolicyVerdictOverridesRaw(t *testing.T) {
	yes, no := true, false
	// Account a still has 8% weekly, which its profile deems too little; b has
	// less raw quota but its profile (a bigger plan) still accepts it.
	f := &fakeQuota{data: map[string][]llms.ModelUsage{
		"a": {{Model: "claude", Remaining: 0.08, Usable: &no, Reason: "weekly: 8% remaining"}},
		"b": {{Model: "claude", Remaining: 0.04, Usable: &yes}},
	}}
	setupSelector(t, []string{"a", "b"}, f)

	alias, ok, err := chooseAccount("claude", "claude")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", alias)
}

func TestSetProfileEnv(t *testing.T) {
	old := readAccountProfile
	t.Cleanup(func() { readAccountProfile = old })
	readAccountProfile = func(cli, alias string) (string, error) {
		if alias == "bound" {
			return "pro20", nil
		}
		return "", nil
	}

	envs := map[string]string{}
	setProfileEnv(envs, "agy", accountSel{alias: "bound", ok: true}, true)
	assert.Equal(t, map[string]string{"ASGARD_AGY_PROFILE": "pro20"}, envs)

	envs = map[string]string{}
	setProfileEnv(envs, "agy", accountSel{alias: "unbound", ok: true}, true)
	setProfileEnv(envs, "agy", accountSel{ok: false}, true)
	setProfileEnv(envs, "agy", accountSel{alias: "bound", ok: true}, false) // agent sandbox
	assert.Empty(t, envs)
}

func TestAllAccountUsages(t *testing.T) {
	f := &fakeQuota{
		data: map[string][]llms.ModelUsage{
			"a": {{Model: "claude", Remaining: 0.1}},
			"b": {{Model: "claude", Remaining: 0.7}},
		},
		errs: map[string]error{"c": errors.New("no creds")},
	}
	setupSelector(t, []string{"a", "b", "c"}, f)

	usages, ok := AllAccountUsages("claude")
	require.True(t, ok)
	require.Len(t, usages, 2, "the account that failed is skipped")

	// An account override means no selection is in play.
	t.Setenv("ASGARD_CLAUDE_ACCOUNT", "a")
	_, ok = AllAccountUsages("claude")
	assert.False(t, ok)
}

func TestAllAccountUsages_SingleAccountOrNoAnswer(t *testing.T) {
	setupSelector(t, []string{"a"}, &fakeQuota{data: map[string][]llms.ModelUsage{"a": {{Model: "claude"}}}})
	_, ok := AllAccountUsages("claude")
	assert.False(t, ok, "one account: the plain query is enough")

	setupSelector(t, []string{"a", "b"}, &fakeQuota{errs: map[string]error{"a": errors.New("x"), "b": errors.New("y")}})
	_, ok = AllAccountUsages("claude")
	assert.False(t, ok, "no account answered")
}
