package bwrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/accounts"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// The golden tests pin the complete expanded bwrap argv (including the --args
// file content) of every exported sandbox constructor over a matrix of inputs,
// so refactors of the builders can be proven behavior-neutral. Regenerate with
// UPDATE_GOLDEN=1 go test ./pkg/bwrap -run TestGolden.

type goldenFixture struct {
	root      string
	home      string
	runA      string
	runB      string
	runUnder  string // a run dir beneath HOME
	globRun   string // glob matching run dirs
	roDir     string
	rwDir     string
	rwGlob    string
	roGlob    string
	maskedRW  string
	agentPath string
	cfgPath   string
	caCert    string
	caKey     string
	worktree  *agentspec.WorktreeRun
}

func newGoldenFixture(t *testing.T) *goldenFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := &goldenFixture{root: root, home: filepath.Join(root, "home")}
	t.Setenv("HOME", f.home)
	t.Setenv("ASGARD_ROOT", filepath.Join(f.home, "asgard"))
	t.Setenv("ASGARD_SESSION_DATA", filepath.Join(f.home, "asgard", "session-data"))
	t.Setenv("ASGARD_ACCOUNTS_DIR", filepath.Join(root, "accounts"))
	t.Setenv("ASGARD_AGY_ACCOUNT", "")
	t.Setenv("ASGARD_CLAUDE_ACCOUNT", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("TZ", "UTC")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pathbin"), 0o755))
	t.Setenv("PATH", "/usr/bin:/bin:"+filepath.Join(root, "pathbin"))
	// Deterministic inherited environment: drop every allowlisted host
	// variable, then set a fixed few.
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && inheritedEnvKey(k) {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}
	t.Setenv("AW_CONFIG", filepath.Join(f.home, ".config", "aw", "config.yaml"))
	t.Setenv("OPENCODE_AW_CONFIG", filepath.Join(f.home, ".config", "aw", "opencode.aw.yaml"))
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("USER", "golden")

	mk := func(parts ...string) string {
		p := filepath.Join(parts...)
		require.NoError(t, os.MkdirAll(p, 0o755))
		return p
	}
	for _, sub := range []string{".gemini", ".claude", ".codex", ".cache", ".local", ".ssh", "asgard/config", ".config/aw", ".config/gcp-sa.d"} {
		mk(f.home, sub)
	}
	write := func(p, s string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(s), 0o600))
	}
	write(filepath.Join(f.home, ".config", "gcp-sa.json"), "{}")
	write(filepath.Join(f.home, ".config", "aw", "config.yaml"), "x: 1\n")
	write(filepath.Join(f.home, ".config", "aw", "opencode.aw.yaml"), "y: 1\n")
	write(filepath.Join(f.home, ".gitconfig"), "[user]\n")
	write(filepath.Join(f.home, "asgard", "config", "ca", "ca.key"), "key")

	// accounts: exactly one per CLI so no quota lookup is ever triggered.
	write(filepath.Join(root, "accounts", "agy", "a", "antigravity-oauth-token"), "{}")
	write(filepath.Join(root, "accounts", "claude", "a", accounts.ClaudeCredentialsFile), "{}")

	ws := filepath.Join(root, "ws")
	f.runA = mk(ws, "runA")
	f.runB = mk(ws, "runB")
	f.runUnder = mk(f.home, "proj", "under")
	mk(ws, "globs", "proj-1", "sub")
	mk(ws, "globs", "proj-2")
	write(filepath.Join(ws, "globs", "not-a-dir"), "x")
	f.globRun = filepath.Join(ws, "globs", "proj-*")
	f.roDir = mk(ws, "ro")
	f.rwDir = mk(ws, "rw")
	mk(ws, "rwglob", "a")
	mk(ws, "rwglob", "b")
	f.rwGlob = filepath.Join(ws, "rwglob", "*")
	mk(ws, "roglob-1")
	mk(ws, "roglob-2")
	f.roGlob = filepath.Join(ws, "roglob-*")
	f.maskedRW = mk(ws, "masked")
	mk(ws, "masked", "runin")
	mk(ws, "masked", "keep")
	mk(ws, "masked", "other")
	mk(ws, "masked", "secret")
	write(filepath.Join(ws, "masked", "file.txt"), "x")

	f.agentPath = mk(root, "agent")
	write(filepath.Join(f.agentPath, "AGENTS.md"), "# agent instructions\n")
	mk(f.agentPath, "skills")
	f.cfgPath = filepath.Join(f.home, "asgard", "config", "config.yaml")
	write(f.cfgPath, "secret: 1\n")
	f.caCert = filepath.Join(root, "ca.crt")
	write(f.caCert, "-----BEGIN CERTIFICATE-----\nZ29sZGVu\n-----END CERTIFICATE-----\n")
	f.caKey = filepath.Join(f.home, "asgard", "config", "ca", "ca.key")

	wr := &agentspec.WorktreeRun{
		GitDir:       filepath.Join(root, "repo", ".git"),
		Name:         "repo-c1",
		RefNamespace: "asgard/c1",
		ObjectsDir:   filepath.Join(root, "priv", "c1"),
	}
	for _, d := range []string{
		filepath.Join(wr.GitDir, "worktrees", wr.Name),
		filepath.Join(wr.GitDir, "refs", "heads", "asgard", "c1"),
		filepath.Join(wr.GitDir, "logs", "refs", "heads", "asgard", "c1"),
		filepath.Join(wr.GitDir, "objects"),
		wr.ObjectsDir,
	} {
		mk(d)
	}
	f.worktree = wr
	return f
}

func (f *goldenFixture) cfgRW() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{
		ID: "rw", Name: "rw", Description: "rw",
		RunDirs: []string{f.runA, f.globRun, f.runA},
		MountDirs: agentspec.MountConfig{
			ReadOnly:  []string{f.roDir, f.roGlob},
			ReadWrite: []string{f.rwDir, f.rwGlob},
			ReadWriteEntries: []agentspec.MountPathEntry{
				{Path: f.maskedRW, MaskRules: &agentspec.SubdirMaskRule{
					Mode: agentspec.MaskOthers, KeepRW: []string{"keep"},
				}},
			},
			MaskRules: map[string]*agentspec.SubdirMaskRule{
				f.runB: {Mode: agentspec.MaskMatched, MaskRO: []string{"secret*"}},
			},
		},
		SharedChatID: "root-1",
	}
}

func (f *goldenFixture) cfgRWMaskedRun() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{
		ID: "m", Name: "m", Description: "m",
		RunDirs: []string{f.runB},
		MountDirs: agentspec.MountConfig{
			ReadWrite: []string{f.maskedRW},
			MaskRules: map[string]*agentspec.SubdirMaskRule{
				f.maskedRW: {Mode: agentspec.MaskOthers, KeepRW: []string{"keep", "runin"}},
			},
		},
	}
}

func (f *goldenFixture) cfgMaskMatched() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{
		ID: "mm", Name: "mm", Description: "mm",
		RunDirs: []string{f.runA},
		MountDirs: agentspec.MountConfig{
			ReadWrite: []string{f.maskedRW},
			MaskRules: map[string]*agentspec.SubdirMaskRule{
				f.maskedRW: {Mode: agentspec.MaskMatched, MaskRO: []string{"^secret$"}},
			},
		},
	}
}

func (f *goldenFixture) cfgWorktree() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{
		ID: "wt", Name: "wt", Description: "wt",
		RunDirs:     []string{f.runA},
		WorktreeRun: f.worktree,
	}
}

func (f *goldenFixture) cfgRO() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{
		ID: "ro", Name: "ro", Description: "ro",
		LockMode: agentspec.LockModeReadOnly,
		RunDirs:  []string{f.runA, f.globRun, f.runUnder},
		MountDirs: agentspec.MountConfig{
			ReadOnly:  []string{f.roDir, f.roGlob},
			ReadWrite: []string{f.rwDir},
		},
		SharedChatID: "root-2",
	}
}

func (f *goldenFixture) cfgMin() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{ID: "min", Name: "min", Description: "min", RunDirs: []string{f.runA}}
}

func (f *goldenFixture) cfgBadRun() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{ID: "bad", Name: "bad", Description: "bad", RunDirs: []string{filepath.Join(f.root, "ws", "missing")}}
}

func (f *goldenFixture) cfgBadRW() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{ID: "bad", Name: "bad", Description: "bad", RunDirs: []string{f.runA},
		MountDirs: agentspec.MountConfig{ReadWrite: []string{filepath.Join(f.root, "ws", "missing-rw")}}}
}

func (f *goldenFixture) cfgRelRO() *agentspec.AgentConfig {
	return &agentspec.AgentConfig{ID: "bad", Name: "bad", Description: "bad", RunDirs: []string{f.runA},
		MountDirs: agentspec.MountConfig{ReadOnly: []string{"relative/dir"}}}
}

func (f *goldenFixture) cfgROBeneathRW() *agentspec.AgentConfig {
	// A readonly root inside a run dir and a run dir beneath a readonly root.
	return &agentspec.AgentConfig{ID: "x", Name: "x", Description: "x",
		RunDirs:   []string{f.runA, f.roDir},
		MountDirs: agentspec.MountConfig{ReadOnly: []string{f.roDir}, ReadWrite: []string{f.rwDir}}}
}

func (f *goldenFixture) proxy(multiEnv bool) ProxySandboxConfig {
	env := map[string]string{"DUMMY_KEY": "dummy-secret"}
	if multiEnv {
		env["A_KEY"] = "a"
		env["Z_KEY"] = "z"
		env[" "] = "blank"
	}
	return ProxySandboxConfig{
		Enabled: true, ProxyAddr: "http://127.0.0.1:8082",
		CACert: f.caCert, CAKey: f.caKey, EnvVars: env,
		ExtraEnv: []string{"ASGARD_RUN_TOKEN=tok", "HOME=/evil", "FOO=bar"},
	}
}

func (f *goldenFixture) proxyOffWithEnv() ProxySandboxConfig {
	return ProxySandboxConfig{ExtraEnv: []string{"ASGARD_RUN_TOKEN=tok", "FOO=bar"}, CAKey: f.caKey}
}

var goldenChatIDRe = regexp.MustCompile(`aw-query-\d+-\d+`)

func (f *goldenFixture) render(cmd *exec.Cmd, err error) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "ERROR: %v\n", err)
	}
	if cmd != nil {
		for _, a := range bwrapArgsOf(cmd) {
			b.WriteString(a)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "#env=%d extrafiles=%d\n", len(cmd.Env), len(cmd.ExtraFiles))
		CloseArgs(cmd)
	}
	return f.normalize(b.String())
}

func (f *goldenFixture) renderArgs(args []string, err error) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "ERROR: %v\n", err)
	}
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte('\n')
	}
	return f.normalize(b.String())
}

func (f *goldenFixture) normalize(s string) string {
	s = strings.ReplaceAll(s, f.root, "<ROOT>")
	s = goldenChatIDRe.ReplaceAllString(s, "aw-query-X")
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		// Host system mounts (/usr, /lib, /etc, /bin, localtime target, the
		// optional /etc/ssl/cert.pem alias, fakebash destinations, and optional
		// fish masking) differ per machine.
		if i+2 < len(lines) && lines[i] == "--ro-bind" {
			if lines[i+2] == "/etc/ssl/cert.pem" {
				i += 2
				continue
			}
			if lines[i+1] == "/bin/fakebash" {
				i += 2
				continue
			}
			if lines[i+1] == "/dev/null" && filepath.Base(lines[i+2]) == "fish" {
				i += 2
				continue
			}
			if p := lines[i+1]; p == lines[i+2] && (strings.HasPrefix(p, "/usr/") || strings.HasPrefix(p, "/lib") || p == "/etc" || p == "/bin") {
				i += 2
				continue
			}
		}
		out = append(out, lines[i])
	}
	return f.sortAuthMasks(out)
}

// sortAuthMasks sorts contiguous runs of masks of CLI auth directories/files:
// SandboxSpec.cmdExecArgs visits the registered CLIs in map order, which is not
// deterministic. Every other mask keeps its position.
func (f *goldenFixture) sortAuthMasks(lines []string) string {
	var authDirs []string
	for _, cli := range agentwrapper.GetRegisteredCLIs() {
		if sp := agentwrapper.GetSandboxSpec(cli); sp != nil {
			if d := sp.AuthDirectory(f.home); d != "" {
				authDirs = append(authDirs, strings.ReplaceAll(d, f.root, "<ROOT>"))
			}
		}
	}
	isAuth := func(p string) bool {
		for _, d := range authDirs {
			if p == d || strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return false
	}
	// entry reports the length of an auth mask entry starting at i, or 0.
	entry := func(i int) int {
		if i+1 < len(lines) && lines[i] == "--tmpfs" && isAuth(lines[i+1]) {
			return 2
		}
		if i+2 < len(lines) && lines[i] == "--ro-bind" && lines[i+1] == "/dev/null" && isAuth(lines[i+2]) {
			return 3
		}
		return 0
	}
	var out []string
	for i := 0; i < len(lines); {
		if entry(i) == 0 {
			out = append(out, lines[i])
			i++
			continue
		}
		var run []string
		for n := entry(i); n > 0; n = entry(i) {
			run = append(run, strings.Join(lines[i:i+n], "\x00"))
			i += n
		}
		sort.Strings(run)
		for _, r := range run {
			out = append(out, strings.Split(r, "\x00")...)
		}
	}
	return strings.Join(out, "\n")
}

type goldenCase struct {
	name string
	run  func(f *goldenFixture) string
}

func goldenCases() []goldenCase {
	var cases []goldenCase
	add := func(name string, run func(f *goldenFixture) string) {
		cases = append(cases, goldenCase{name, run})
	}

	// --- CommandForAgent ---
	type cfgMaker struct {
		name string
		mk   func(f *goldenFixture) *agentspec.AgentConfig
	}
	cfgs := []cfgMaker{
		{"nilcfg", func(*goldenFixture) *agentspec.AgentConfig { return nil }},
		{"min", (*goldenFixture).cfgMin},
		{"rw", (*goldenFixture).cfgRW},
		{"maskedrun", (*goldenFixture).cfgRWMaskedRun},
		{"ro", (*goldenFixture).cfgRO},
		{"robeneath", (*goldenFixture).cfgROBeneathRW},
		{"badrun", (*goldenFixture).cfgBadRun},
		{"badrw", (*goldenFixture).cfgBadRW},
		{"relro", (*goldenFixture).cfgRelRO},
	}
	targets := []agentspec.CLITarget{
		{CLI: "agy", Model: "gemini-x"},
		{CLI: "claude", Model: "sonnet"},
		{CLI: "opencode", Model: "oc/model"},
		{},
	}
	for _, c := range cfgs {
		for _, tg := range targets {
			if c.name != "rw" && c.name != "ro" && tg.CLI != "agy" && tg.CLI != "" {
				continue
			}
			c, tg := c, tg
			for _, px := range []string{"noproxy", "proxy", "proxyoff"} {
				if px != "noproxy" && (c.name != "rw" && c.name != "ro" && c.name != "nilcfg") {
					continue
				}
				if px != "noproxy" && tg.CLI != "agy" && tg.CLI != "claude" {
					continue
				}
				px := px
				add(fmt.Sprintf("agent/%s/%s/%s", c.name, tg.CLI, px), func(f *goldenFixture) string {
					var proxies []ProxySandboxConfig
					switch px {
					case "proxy":
						proxies = []ProxySandboxConfig{f.proxy(true)}
					case "proxyoff":
						proxies = []ProxySandboxConfig{f.proxyOffWithEnv()}
					}
					cmd, err := CommandForAgent(c.mk(f), f.agentPath, tg, "do the thing", optional.Some("sess-1"), f.runB, "/sock/dir", "agent-chat", "## Lang\nrules", f.cfgPath, proxies...)
					return f.render(cmd, err)
				})
			}
		}
	}
	add("agent/rw/agy/nosession-noprompt-nosock", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgRW(), "", agentspec.CLITarget{CLI: "agy", Model: "m"}, "", optional.None[string](), f.runUnder, "", "", "", "")
		return f.render(cmd, err)
	})
	add("agent/rw/claude/emptysession", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgRW(), f.agentPath, agentspec.CLITarget{CLI: "claude", Model: "m"}, "p", optional.Some(""), f.runA, "/s", "c2", "", f.cfgPath)
		return f.render(cmd, err)
	})
	add("agent/rw/missingrundir", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgRW(), "", agentspec.CLITarget{CLI: "agy", Model: "m"}, "", optional.None[string](), filepath.Join(f.root, "nope"), "", "c3", "", "")
		return f.render(cmd, err)
	})
	add("agent/ro/rundirbeneathrodirs", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgRO(), "", agentspec.CLITarget{CLI: "agy", Model: "m"}, "", optional.None[string](), filepath.Join(f.root, "ws", "globs", "proj-1", "sub"), "", "c4", "", "")
		return f.render(cmd, err)
	})
	add("agent/rw/rundirbeneathrwdirs", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgRW(), "", agentspec.CLITarget{CLI: "agy", Model: "m"}, "", optional.None[string](), filepath.Join(f.root, "ws", "globs", "proj-1", "sub"), "", "c5", "", "")
		return f.render(cmd, err)
	})
	add("agent/rw/rundirunderhome", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgRW(), "", agentspec.CLITarget{CLI: "agy", Model: "m"}, "", optional.None[string](), f.runUnder, "", "c6", "", "")
		return f.render(cmd, err)
	})
	add("agent/maskmatched/agy/noproxy", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgMaskMatched(), f.agentPath, agentspec.CLITarget{CLI: "agy", Model: "gemini-x"}, "do the thing", optional.Some("sess-1"), f.runA, "/sock/dir", "agent-chat", "", f.cfgPath)
		return f.render(cmd, err)
	})
	add("agent/worktree/agy/noproxy", func(f *goldenFixture) string {
		cmd, err := CommandForAgent(f.cfgWorktree(), f.agentPath, agentspec.CLITarget{CLI: "agy", Model: "gemini-x"}, "do the thing", optional.Some("sess-1"), f.runA, "/sock/dir", "agent-chat", "", f.cfgPath)
		return f.render(cmd, err)
	})

	// --- CommandForAgentCommandExec / CommandForCommandExec(At) ---
	for _, c := range cfgs {
		for _, cross := range []bool{false, true} {
			for _, px := range []string{"noproxy", "proxy", "proxyoff"} {
				if px != "noproxy" && c.name != "rw" && c.name != "ro" && c.name != "nilcfg" {
					continue
				}
				if !cross && px == "proxyoff" {
					continue
				}
				c, cross, px := c, cross, px
				add(fmt.Sprintf("agentcmd/%s/cross=%v/%s", c.name, cross, px), func(f *goldenFixture) string {
					var proxies []ProxySandboxConfig
					switch px {
					case "proxy":
						proxies = []ProxySandboxConfig{f.proxy(false)}
					case "proxyoff":
						proxies = []ProxySandboxConfig{f.proxyOffWithEnv()}
					}
					cmd, err := CommandForAgentCommandExec(c.mk(f), f.runB, "", "/sock/dir", "cmd-chat", f.cfgPath, cross, proxies...)
					return f.render(cmd, err)
				})
			}
		}
	}
	add("agentcmd/rw/cwd-and-norundir", func(f *goldenFixture) string {
		cmd, err := CommandForAgentCommandExec(f.cfgRW(), f.runA, filepath.Join(f.runA, "sub"), "", "cmd2", "", false)
		out := f.render(cmd, err)
		cmd, err = CommandForAgentCommandExec(f.cfgRW(), "", "", "", "cmd2", "", false)
		out += "---\n" + f.render(cmd, err)
		cmd, err = CommandForAgentCommandExec(f.cfgRW(), filepath.Join(f.root, "nope"), "", "", "cmd2", "", false)
		return out + "---\n" + f.render(cmd, err)
	})
	add("agentcmd/ro/cwd-and-norundir", func(f *goldenFixture) string {
		cmd, err := CommandForAgentCommandExec(f.cfgRO(), f.runA, filepath.Join(f.runA, "sub"), "", "cmd3", "", true)
		out := f.render(cmd, err)
		cmd, err = CommandForAgentCommandExec(f.cfgRO(), "", "", "", "cmd3", "", false)
		out += "---\n" + f.render(cmd, err)
		cmd, err = CommandForAgentCommandExec(f.cfgRO(), f.runUnder, "", "", "cmd3", "", false)
		out += "---\n" + f.render(cmd, err)
		cmd, err = CommandForAgentCommandExec(f.cfgRO(), filepath.Join(f.root, "nope"), "", "", "cmd3", "", false)
		return out + "---\n" + f.render(cmd, err)
	})
	add("agentcmd/maskedrun/rundirs", func(f *goldenFixture) string {
		out := ""
		for _, rd := range []string{f.runB, filepath.Join(f.maskedRW, "runin"), f.runUnder} {
			cmd, err := CommandForAgentCommandExec(f.cfgRWMaskedRun(), rd, "", "", "cmd4", "", false)
			out += "---\n" + f.render(cmd, err)
		}
		return out
	})
	add("agentcmd/maskmatched/cross=false/noproxy", func(f *goldenFixture) string {
		cmd, err := CommandForAgentCommandExec(f.cfgMaskMatched(), f.runA, "", "/sock/dir", "cmd-chat", f.cfgPath, false)
		return f.render(cmd, err)
	})
	add("agentcmd/worktree/cross=false/noproxy", func(f *goldenFixture) string {
		cmd, err := CommandForAgentCommandExec(f.cfgWorktree(), f.runA, "", "/sock/dir", "cmd-chat", f.cfgPath, false)
		return f.render(cmd, err)
	})

	for _, cross := range []bool{false, true} {
		for _, px := range []string{"noproxy", "proxy"} {
			cross, px := cross, px
			add(fmt.Sprintf("cmd/cross=%v/%s", cross, px), func(f *goldenFixture) string {
				var proxies []ProxySandboxConfig
				if px == "proxy" {
					proxies = []ProxySandboxConfig{f.proxy(false)}
				}
				out := ""
				for _, rd := range []string{f.runA, f.runUnder, "", filepath.Join(f.root, "nope")} {
					cmd, err := CommandForCommandExec(rd, "/sock/dir", "plain-chat", f.cfgPath, cross, proxies...)
					out += "---\n" + f.render(cmd, err)
				}
				return out
			})
		}
	}
	add("cmdat/readonlydirs", func(f *goldenFixture) string {
		out := ""
		cases := []struct {
			run, cwd string
			ro       []string
		}{
			{f.runA, "", []string{f.roDir, f.roGlob}},
			{f.runA, filepath.Join(f.runA, "sub"), []string{f.roDir}},
			{f.roDir, "", []string{f.roDir}},
			{filepath.Join(f.roDir, "x"), "", []string{f.roDir}},
			{f.runA, "", []string{"relative"}},
			{f.runA, "", []string{filepath.Join(f.root, "gone")}},
			{f.runA, "", []string{"/tmp"}},
			{f.runA, "", []string{filepath.Join(f.home, ".ssh", "x")}},
			{f.runUnder, "", []string{f.runUnder}},
		}
		for _, c := range cases {
			cmd, err := CommandForCommandExecAt(c.run, c.cwd, "", "at-chat", "", false, c.ro, f.proxy(false))
			out += "---\n" + f.render(cmd, err)
		}
		cmd, err := CommandForCommandExecAt(f.runA, "", "", "at-chat", "", true, []string{f.roDir})
		return out + "---\n" + f.render(cmd, err)
	})

	// --- Query ---
	add("query/unpinned", func(f *goldenFixture) string {
		cmd, cleanup, err := CommandForQuery(context.Background(), "models", "--json")
		if cleanup != nil {
			defer cleanup()
		}
		return f.render(cmd, err)
	})
	add("query/pinned", func(f *goldenFixture) string {
		out := ""
		for _, pins := range []map[string]string{
			{"agy": "a"}, {"claude": "a"}, {"agy": "a", "claude": "a"}, {"agy": "ghost"}, {"claude": "ghost"},
		} {
			cmd, cleanup, err := CommandForQueryWithAccounts(context.Background(), pins, "usage")
			if cleanup != nil {
				defer cleanup()
			}
			out += "---\n" + f.render(cmd, err)
		}
		return out
	})
	add("query/noawconfig", func(f *goldenFixture) string {
		require.NoError(nopT{}, os.Remove(filepath.Join(f.home, ".config", "aw", "opencode.aw.yaml")))
		cmd, cleanup, err := CommandForQuery(context.Background(), "models")
		if cleanup != nil {
			defer cleanup()
		}
		return f.render(cmd, err)
	})

	// --- Git ---
	for _, net := range []bool{false, true} {
		for _, rw := range []bool{false, true} {
			for _, px := range []string{"noproxy", "proxy"} {
				net, rw, px := net, rw, px
				add(fmt.Sprintf("git/net=%v/rw=%v/%s", net, rw, px), func(f *goldenFixture) string {
					spec := GitSandboxSpec{
						ChatID: "git-chat", ReadWrite: rw, Network: net,
						Env: []string{"GIT_AUTHOR_NAME=x", "PATH=/evil"}, ConfigPath: f.cfgPath,
						Git: []string{"status", "--short"},
					}
					if px == "proxy" {
						spec.Proxy = f.proxy(false)
					}
					spec.WorkTreeSrc = "/proc/self/fd/9"
					args, err := GitSandboxArgs(spec)
					out := f.renderArgs(args, err)
					cmd, err := CommandForGit(context.Background(), spec)
					return out + "---\n" + f.render(cmd, err)
				})
			}
		}
	}
	return cases
}

// nopT lets fixture-mutating cases assert without a *testing.T.
type nopT struct{}

func (nopT) Errorf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
func (nopT) FailNow()                          { panic("fail") }

func TestGolden(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			f := newGoldenFixture(t)
			got := c.run(f)
			path := filepath.Join("testdata", "golden", strings.ReplaceAll(c.name, "/", "__")+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "missing golden file; run with UPDATE_GOLDEN=1")
			require.Equal(t, string(want), got)
		})
	}
}
