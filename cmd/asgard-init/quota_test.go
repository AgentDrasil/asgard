package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readAWConfig(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "etcd-tree", "aw", "config.yaml"))
	require.NoError(t, err)
	return string(data)
}

func mustProfile(t *testing.T, spec string) quotaProfile {
	t.Helper()
	p, err := parseQuotaProfile(spec)
	require.NoError(t, err)
	return p
}

func TestParseQuotaProfile(t *testing.T) {
	p := mustProfile(t, "claude-20:5h=0.10,weekly=0.05")
	assert.Equal(t, "claude-20", p.Name)
	assert.Equal(t, []quotaThreshold{{"5h", 0.10}, {"weekly", 0.05}}, p.Thresholds)
	assert.False(t, p.Unlimited)

	t.Run("a plan with a single window", func(t *testing.T) {
		assert.Equal(t, []quotaThreshold{{"weekly", 0.08}}, mustProfile(t, "agy-50:weekly=0.08").Thresholds)
	})
	t.Run("window names are free-form", func(t *testing.T) {
		got := mustProfile(t, `x:TOKENS_LIMIT=0.2, weekly:sonnet only=0.1 ,*=0.05, weekly*=0`)
		assert.Equal(t, []quotaThreshold{{"TOKENS_LIMIT", 0.2}, {"weekly:sonnet only", 0.1}, {"*", 0.05}, {"weekly*", 0}}, got.Thresholds)
	})
	t.Run("unlimited", func(t *testing.T) {
		got := mustProfile(t, "ppr:unlimited")
		assert.True(t, got.Unlimited)
		assert.Empty(t, got.Thresholds)
	})

	for name, spec := range map[string]string{
		"no colon":          "claude-20",
		"no thresholds":     "claude-20:",
		"bad name":          "Claude 20:5h=0.1",
		"no ratio":          "a:5h",
		"ratio not number":  "a:5h=lots",
		"ratio above one":   "a:5h=1.5",
		"negative ratio":    "a:5h=-0.1",
		"empty window":      "a:=0.1",
		"duplicate window":  "a:5h=0.1,5h=0.2",
		"empty name":        ":5h=0.1",
		"name too long":     strings.Repeat("a", 33) + ":5h=0.1",
		"unlimited in list": "a:unlimited,5h=0.1",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := parseQuotaProfile(spec)
			assert.Error(t, err)
		})
	}
}

func TestNormalizeQuotaProfiles(t *testing.T) {
	got, err := normalizeQuotaProfiles([]string{" claude-20 : 5h=0.10 , weekly=0.050", "", "ppr:unlimited"})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-20:5h=0.1,weekly=0.05", "ppr:unlimited"}, got, "canonical form")

	_, err = normalizeQuotaProfiles([]string{"a:5h=0.1", "a:weekly=0.1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defined twice")

	_, err = normalizeQuotaProfiles([]string{"nope"})
	require.Error(t, err)
}

func TestQuotaProfileYAMLQuotesKeys(t *testing.T) {
	assert.Equal(t, []string{
		`  "claude-20":`,
		`    thresholds: { "5h": 0.1, "weekly": 0.05 }`,
	}, mustProfile(t, "claude-20:5h=0.1,weekly=0.05").yamlLines("  "))

	// Bare "*" and "weekly:sonnet only" are not valid YAML keys.
	assert.Equal(t,
		`    thresholds: { "*": 0.1, "weekly:sonnet only": 0.2 }`,
		mustProfile(t, "x:*=0.1,weekly:sonnet only=0.2").yamlLines("  ")[1])

	assert.Equal(t, []string{`  "ppr":`, `    unlimited: true`}, mustProfile(t, "ppr:unlimited").yamlLines("  "))
}

func TestRunQuotaProfileFlagRendersAWConfig(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	dir := t.TempDir()

	require.NoError(t, run([]string{
		"--non-interactive", "--dir", dir,
		"--quota-profile", "claude-20:5h=0.10,weekly=0.05",
		"--quota-profile", "agy-50:weekly=0.08",
		"--quota-profile", "ppr:unlimited",
	}))

	cfg := readAWConfig(t, dir)
	assert.Contains(t, cfg, "\nquota_profiles:\n")
	assert.Contains(t, cfg, "  \"claude-20\":\n    thresholds: { \"5h\": 0.1, \"weekly\": 0.05 }")
	assert.Contains(t, cfg, "  \"agy-50\":\n    thresholds: { \"weekly\": 0.08 }")
	assert.Contains(t, cfg, "  \"ppr\":\n    unlimited: true")
	assert.NotContains(t, cfg, "\"default\":", "no default profile unless the operator defines one")
}

func TestRunWithoutQuotaProfilesKeepsOnlyCommentedExample(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	dir := t.TempDir()

	require.NoError(t, run([]string{"--non-interactive", "--dir", dir}))

	cfg := readAWConfig(t, dir)
	for _, line := range strings.Split(cfg, "\n") {
		assert.False(t, strings.HasPrefix(line, "quota_profiles:"), "an active quota_profiles would change behavior: %q", line)
	}
	assert.Contains(t, cfg, "# quota_profiles:")
}

func TestRunRejectsBadQuotaProfileFlags(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")

	err := run([]string{"--non-interactive", "--dir", t.TempDir(), "--quota-profile", "claude-20:5h=lots"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--quota-profile")

	err = run([]string{"--non-interactive", "--dir", t.TempDir(), "--quota-profile", "a:5h=0.1", "--quota-profile", "a:weekly=0.1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defined twice")

	// The valued --quota-profiles of the first version points at the new flag.
	err = run([]string{"--non-interactive", "--dir", t.TempDir(), "--quota-profiles", "pro20"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--quota-profile")
}

func TestPromptQuotaProfiles(t *testing.T) {
	var out bytes.Buffer
	got := PromptQuotaProfiles(strings.NewReader("claude-20\n5h=0.10, weekly=0.05\nppr\nunlimited\n\n"), &out)
	assert.Equal(t, []string{"claude-20:5h=0.1,weekly=0.05", "ppr:unlimited"}, got)
	assert.Contains(t, out.String(), "aw usage --cli <cli>", "tells where the window names come from")

	assert.Empty(t, PromptQuotaProfiles(strings.NewReader("\n"), &out), "empty name finishes with none")
	assert.Empty(t, PromptQuotaProfiles(strings.NewReader(""), &out), "end of input finishes with none")

	t.Run("a bad answer is reported and asked again", func(t *testing.T) {
		var out bytes.Buffer
		got := PromptQuotaProfiles(strings.NewReader("Bad Name\nagy-50\nweekly=lots\nweekly=0.08\n\n"), &out)
		assert.Equal(t, []string{"agy-50:weekly=0.08"}, got)
		assert.Contains(t, out.String(), "invalid quota profile name")
		assert.Contains(t, out.String(), "invalid ratio")
	})
	t.Run("a repeated name is refused", func(t *testing.T) {
		var out bytes.Buffer
		got := PromptQuotaProfiles(strings.NewReader("a\n5h=0.1\na\nb\n5h=0.2\n\n"), &out)
		assert.Equal(t, []string{"a:5h=0.1", "b:5h=0.2"}, got)
		assert.Contains(t, out.String(), "already entered")
	})
	t.Run("end of input in the middle keeps what was complete", func(t *testing.T) {
		got := PromptQuotaProfiles(strings.NewReader("a\n5h=0.1\nb\n"), &bytes.Buffer{})
		assert.Equal(t, []string{"a:5h=0.1"}, got)
	})
}

func TestInitStateRoundTripsQuotaProfiles(t *testing.T) {
	// Save also writes to the working directory; keep it out of the package.
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	s := newInitState()
	s.QuotaProfiles = []string{"claude-20:5h=0.1"}
	require.NoError(t, s.Save(dir))

	_, loaded, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-20:5h=0.1"}, loaded.QuotaProfiles)
}

func TestExtractBareQuotaProfiles(t *testing.T) {
	tests := []struct {
		name string
		args []string
		rest []string
		bare bool
	}{
		{"bare at the end", []string{"--dir", "d", "--quota-profiles"}, []string{"--dir", "d"}, true},
		{"bare before another flag", []string{"--quota-profiles", "--dir", "d"}, []string{"--dir", "d"}, true},
		{"single dash", []string{"-quota-profiles"}, nil, true},
		{"with a value", []string{"--quota-profiles", "pro20"}, []string{"--quota-profiles", "pro20"}, false},
		{"the singular flag is not the bare one", []string{"--quota-profile", "a:5h=0.1"}, []string{"--quota-profile", "a:5h=0.1"}, false},
		{"absent", []string{"--dir", "d"}, []string{"--dir", "d"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, bare := extractBareQuotaProfiles(tt.args)
			assert.Equal(t, tt.rest, rest)
			assert.Equal(t, tt.bare, bare)
		})
	}
}

func TestMergeQuotaProfiles_AppendsSectionWithoutTouchingTheRest(t *testing.T) {
	existing := "# Agentwrapper configuration\n\ncli_fallbacks:\n  agy:\n    - simplest\n"

	got, added, skipped, err := mergeQuotaProfiles(existing, []quotaProfile{mustProfile(t, "claude-20:5h=0.1,weekly=0.05"), mustProfile(t, "ppr:unlimited")})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-20", "ppr"}, added)
	assert.Empty(t, skipped)
	assert.True(t, strings.HasPrefix(got, existing[:len(existing)-1]), "existing content is kept verbatim")
	assert.Contains(t, got, "\nquota_profiles:\n  \"claude-20\":\n    thresholds: { \"5h\": 0.1, \"weekly\": 0.05 }")
	assert.Contains(t, got, "  \"ppr\":\n    unlimited: true\n")
	assert.True(t, strings.HasSuffix(got, "\n"))
}

func TestMergeQuotaProfiles_InsertsIntoExistingSection(t *testing.T) {
	existing := "cli_fallbacks:\n  agy: [simplest]\n\nquota_profiles:\n  default:\n    thresholds: { \"*\": 0.2 }\n  claude-20:\n    thresholds: { 5h: 0.5 }\n\nother: 1\n"

	got, added, skipped, err := mergeQuotaProfiles(existing, []quotaProfile{mustProfile(t, "claude-20:5h=0.1"), mustProfile(t, "agy-50:weekly=0.08")})
	require.NoError(t, err)
	assert.Equal(t, []string{"agy-50"}, added)
	assert.Equal(t, []string{"claude-20"}, skipped, "a profile that exists is not overwritten")
	assert.Contains(t, got, "thresholds: { 5h: 0.5 }", "the operator's claude-20 stays")
	assert.Contains(t, got, "quota_profiles:\n  \"agy-50\":\n    thresholds: { \"weekly\": 0.08 }")
	assert.Contains(t, got, "  default:\n    thresholds: { \"*\": 0.2 }")
	assert.True(t, strings.HasSuffix(got, "\nother: 1\n"))

	// A quoted existing name is recognised too.
	quoted := "quota_profiles:\n  \"claude-20\":\n    unlimited: true\n"
	same, added, skipped, err := mergeQuotaProfiles(quoted, []quotaProfile{mustProfile(t, "claude-20:5h=0.1")})
	require.NoError(t, err)
	assert.Empty(t, added)
	assert.Equal(t, []string{"claude-20"}, skipped)
	assert.Equal(t, quoted, same)
}

func TestMergeQuotaProfiles_FollowsTheSectionsIndent(t *testing.T) {
	existing := "quota_profiles:\n    default:\n        thresholds: { \"*\": 0.2 }\n"
	got, _, _, err := mergeQuotaProfiles(existing, []quotaProfile{mustProfile(t, "ppr:unlimited")})
	require.NoError(t, err)
	assert.Contains(t, got, "quota_profiles:\n    \"ppr\":\n        unlimited: true\n    default:")
}

func TestMergeQuotaProfiles_IgnoresTheCommentedExample(t *testing.T) {
	existing := "# quota_profiles:\n#   claude-20:\n#     thresholds: { \"5h\": 0.10 }\n"
	got, added, skipped, err := mergeQuotaProfiles(existing, []quotaProfile{mustProfile(t, "claude-20:5h=0.1")})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-20"}, added, "a commented example defines nothing")
	assert.Empty(t, skipped)
	assert.Contains(t, got, "\nquota_profiles:\n  \"claude-20\":")
}

func TestMergeQuotaProfiles_RejectsInlineSection(t *testing.T) {
	_, _, _, err := mergeQuotaProfiles("quota_profiles: {}\n", []quotaProfile{mustProfile(t, "a:5h=0.1")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inline")
}

// The generated template and the merge must write the same entries.
func TestMergeQuotaProfiles_MatchesTheTemplate(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	specs := []string{"claude-20:5h=0.1,weekly=0.05", "agy-50:weekly=0.08", "ppr:unlimited"}
	dir := t.TempDir()
	args := []string{"--non-interactive", "--dir", dir}
	for _, s := range specs {
		args = append(args, "--quota-profile", s)
	}
	require.NoError(t, run(args))
	fromTemplate := readAWConfig(t, dir)

	var profiles []quotaProfile
	for _, s := range specs {
		profiles = append(profiles, mustProfile(t, s))
	}
	merged, _, _, err := mergeQuotaProfiles("", profiles)
	require.NoError(t, err)
	for _, p := range profiles {
		for _, line := range p.yamlLines("  ") {
			assert.Contains(t, fromTemplate, line)
			assert.Contains(t, merged, line)
		}
	}
}

func TestRunBareQuotaProfilesUpdatesAnExistingDeployment(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	dir := t.TempDir()
	require.NoError(t, run([]string{"--non-interactive", "--dir", dir}))
	cfgPath := filepath.Join(dir, "etcd-tree", "aw", "config.yaml")
	before := readAWConfig(t, dir)
	require.NotContains(t, before, "\nquota_profiles:")
	composeBefore, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	require.NoError(t, err)

	origStdin := defaultStdinReader
	t.Cleanup(func() { defaultStdinReader = origStdin })
	defaultStdinReader = bufioNewReader(strings.NewReader("claude-20\n5h=0.10,weekly=0.05\nagy-50\nweekly=0.08\n\n"))

	require.NoError(t, run([]string{"--dir", dir, "--quota-profiles"}))

	after := readAWConfig(t, dir)
	assert.True(t, strings.HasPrefix(after, strings.TrimRight(before, "\n")), "the rest of the config is untouched")
	assert.Contains(t, after, "\nquota_profiles:\n  \"claude-20\":")
	assert.Contains(t, after, "  \"agy-50\":")
	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
	composeAfter, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	require.NoError(t, err)
	assert.Equal(t, composeBefore, composeAfter, "nothing else is regenerated")

	// Entering an existing profile again changes nothing.
	defaultStdinReader = bufioNewReader(strings.NewReader("claude-20\n5h=0.5\n\n"))
	require.NoError(t, run([]string{"--dir", dir, "--quota-profiles"}))
	assert.Equal(t, after, readAWConfig(t, dir))
}

func TestRunBareQuotaProfilesWithoutADeploymentOrWithNonInteractive(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	origStdin := defaultStdinReader
	t.Cleanup(func() { defaultStdinReader = origStdin })

	defaultStdinReader = bufioNewReader(strings.NewReader("a\n5h=0.1\n\n"))
	err := run([]string{"--dir", t.TempDir(), "--quota-profiles"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Asgard deployment found")

	err = run([]string{"--dir", t.TempDir(), "--non-interactive", "--quota-profiles"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--quota-profile")

	// An empty answer leaves the file alone.
	dir := t.TempDir()
	require.NoError(t, run([]string{"--non-interactive", "--dir", dir}))
	before := readAWConfig(t, dir)
	defaultStdinReader = bufioNewReader(strings.NewReader("\n"))
	require.NoError(t, run([]string{"--dir", dir, "--quota-profiles"}))
	assert.Equal(t, before, readAWConfig(t, dir))
}
