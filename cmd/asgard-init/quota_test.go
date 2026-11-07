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

func TestParseQuotaProfiles(t *testing.T) {
	got, err := parseQuotaProfiles(" pro20, ppr ,pro20,")
	require.NoError(t, err)
	assert.Equal(t, []string{"pro20", "ppr"}, got, "trimmed, deduplicated, order kept")

	got, err = parseQuotaProfiles("")
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = parseQuotaProfiles("pro20,pro99")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"pro99"`)
	assert.Contains(t, err.Error(), "pro20, max200, ppr")
}

func TestRunQuotaProfilesFlagRendersAWConfig(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	dir := t.TempDir()

	require.NoError(t, run([]string{"--non-interactive", "--dir", dir, "--quota-profiles", "pro20,max200,ppr"}))

	cfg := readAWConfig(t, dir)
	assert.Contains(t, cfg, "quota_profiles:\n")
	assert.Contains(t, cfg, "  pro20:  # $20 plan")
	assert.Contains(t, cfg, "    thresholds: { 5h: 0.1, weekly: 0.05 }")
	assert.Contains(t, cfg, "    thresholds: { 5h: 0.03, weekly: 0.02 }")
	assert.Contains(t, cfg, "  ppr:  # pay-per-request account: always usable\n    unlimited: true")
	assert.NotContains(t, cfg, "default:", "no default profile unless the operator defines one")
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

func TestRunRejectsUnknownQuotaProfile(t *testing.T) {
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")
	err := run([]string{"--non-interactive", "--dir", t.TempDir(), "--quota-profiles", "gold"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--quota-profiles")
}

func TestPromptQuotaProfiles(t *testing.T) {
	var out bytes.Buffer
	assert.Equal(t, []string{"pro20", "ppr"}, PromptQuotaProfiles(strings.NewReader("pro20, ppr\n"), &out))
	assert.Contains(t, out.String(), "max200")

	assert.Empty(t, PromptQuotaProfiles(strings.NewReader("\n"), &out), "empty answer generates none")
	assert.Empty(t, PromptQuotaProfiles(strings.NewReader(""), &out), "end of input generates none")

	out.Reset()
	got := PromptQuotaProfiles(strings.NewReader("gold\nmax200\n"), &out)
	assert.Equal(t, []string{"max200"}, got, "an invalid answer is reported and asked again")
	assert.Contains(t, out.String(), "invalid quota profile")

	out.Reset()
	assert.Empty(t, PromptQuotaProfiles(strings.NewReader("gold"), &out), "invalid answer at end of input gives up")
}

func TestInitStateRoundTripsQuotaProfiles(t *testing.T) {
	// Save also writes to the working directory; keep it out of the package.
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	s := newInitState()
	s.QuotaProfiles = []string{"pro20"}
	require.NoError(t, s.Save(dir))

	_, loaded, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"pro20"}, loaded.QuotaProfiles)
}
