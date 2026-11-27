package bwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

const regexMetaChars = `^$*+?()[]{}|\`

func containsRegexMeta(s string) bool {
	return strings.ContainsAny(s, regexMetaChars)
}

func isLiteralSubdirName(p string) bool {
	if p == "" || p == "." || p == ".." {
		return false
	}
	if containsRegexMeta(p) {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	clean := filepath.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

type compiledMaskPattern struct {
	raw      string
	isRegex  bool
	matchRel bool
	re       *regexp.Regexp
}

func compileMaskPatterns(patterns []string) []compiledMaskPattern {
	if len(patterns) == 0 {
		return nil
	}
	compiled := make([]compiledMaskPattern, 0, len(patterns))
	for _, p := range patterns {
		cp := compiledMaskPattern{
			raw:      p,
			isRegex:  containsRegexMeta(p),
			matchRel: strings.Contains(p, "/"),
		}
		if cp.isRegex {
			if re, err := regexp.Compile(p); err == nil {
				cp.re = re
			}
		}
		compiled = append(compiled, cp)
	}
	return compiled
}

func matchesCompiledMaskPatterns(name, relFromBase string, patterns []compiledMaskPattern) bool {
	for _, p := range patterns {
		if p.raw == name || p.raw == relFromBase {
			return true
		}
		if p.isRegex && p.re != nil {
			target := name
			if p.matchRel {
				target = relFromBase
			}
			if p.re.MatchString(target) {
				return true
			}
		}
	}
	return false
}

type spineStep struct {
	scanDir     string
	activeChild string
}

func computeSpine(cleanBase, cleanRun string) []spineStep {
	if cleanRun == cleanBase || !beneathAny([]string{cleanBase}, cleanRun) {
		return []spineStep{{scanDir: cleanBase, activeChild: ""}}
	}
	rel, err := filepath.Rel(cleanBase, cleanRun)
	if err != nil {
		return []spineStep{{scanDir: cleanBase, activeChild: ""}}
	}
	parts := strings.Split(rel, string(filepath.Separator))
	var spine []spineStep
	cur := cleanBase
	for i := 0; i < len(parts); i++ {
		active := filepath.Join(cur, parts[i])
		if cur == cleanRun {
			break
		}
		spine = append(spine, spineStep{
			scanDir:     cur,
			activeChild: active,
		})
		cur = active
	}
	return spine
}

func applySubdirMaskRule(args []string, baseDir string, rule *agentspec.SubdirMaskRule, runDir string) ([]string, error) {
	if rule == nil {
		return args, nil
	}

	cleanBase := filepath.Clean(baseDir)
	cleanRun := filepath.Clean(runDir)

	// 1. Pre-create literal keep_rw directories
	for _, p := range rule.KeepRW {
		if isLiteralSubdirName(p) {
			targetDir := filepath.Join(cleanBase, p)
			_ = os.MkdirAll(targetDir, 0755)
		}
	}

	keepRWPatterns := compileMaskPatterns(rule.KeepRW)
	maskROPatterns := compileMaskPatterns(rule.MaskRO)

	// 2. Walk the ancestor spine from baseDir to runDir
	spine := computeSpine(cleanBase, cleanRun)

	for _, step := range spine {
		entries, err := os.ReadDir(step.scanDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			itemPath := filepath.Join(step.scanDir, name)
			relFromBase, err := filepath.Rel(cleanBase, itemPath)
			if err != nil {
				continue
			}

			// Step a: Active runDir branch check
			if itemPath == cleanRun {
				continue // skip (keep runDir RW!)
			}
			if step.activeChild != "" && itemPath == step.activeChild {
				continue // skip masking itemPath itself (will descend into it)
			}

			// Step b: keep_rw check
			if matchesCompiledMaskPatterns(name, relFromBase, keepRWPatterns) {
				continue // skip (keep RW!)
			}

			// Step c: Mode evaluation
			var shouldMask bool
			switch rule.Mode {
			case agentspec.MaskOthers:
				shouldMask = true
			case agentspec.MaskMatched:
				shouldMask = matchesCompiledMaskPatterns(name, relFromBase, maskROPatterns)
			}

			if !shouldMask {
				continue
			}

			// Step d: Symlink safety & --ro-bind
			fi, err := os.Lstat(itemPath)
			if err != nil {
				continue
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(itemPath)
				if err != nil {
					// dangling symlink cannot be bound by bwrap
					continue
				}
				// bubblewrap follows symlinks at mount time, so mounting over an
				// escaping symlink would either fail (if the target is not mounted
				// in the sandbox) or clobber the external target's mount point.
				if !beneathAny([]string{cleanBase}, resolved) {
					continue
				}
				targetFi, err := os.Stat(resolved)
				if err != nil || (!targetFi.IsDir() && !targetFi.Mode().IsRegular()) {
					continue
				}
				args = append(args, "--ro-bind", resolved, itemPath)
			} else {
				if !fi.IsDir() && !fi.Mode().IsRegular() {
					continue
				}
				args = append(args, "--ro-bind", itemPath, itemPath)
			}
		}
	}

	return args, nil
}

func expandGlobDirs(entry string) ([]string, bool) {
	if !strings.ContainsAny(entry, "*?[") {
		return nil, false
	}
	matches, err := filepath.Glob(entry)
	if err != nil {
		return nil, true
	}
	var dirs []string
	for _, m := range matches {
		if fi, err := os.Lstat(m); err == nil && fi.IsDir() {
			dirs = append(dirs, filepath.Clean(m))
		}
	}
	return dirs, true
}

// SandboxCwd returns the path at which the host directory dir is visible
// inside a command sandbox of chatID: the chat's tmp and session dirs are
// mounted at /tmp and /session, anything else under the host cross-session data root
// (/session-data) is not mounted unless allowCrossSession (or explicitly mounted read-only),
// and every other directory keeps its path (CommandForCommandExecAt binds it there).
func SandboxCwd(chatID, dir string, allowCrossSession bool, readonlyDirs []string) (string, error) {
	dir = filepath.Clean(dir)
	for _, m := range []struct{ host, mount string }{
		{runnerpaths.SessionTmpDir(chatID), "/tmp"},
		{runnerpaths.SessionDir(chatID), "/session"},
	} {
		for _, base := range realAndLexical(m.host) {
			if rel, err := filepath.Rel(base, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return filepath.Join(m.mount, rel), nil
			}
		}
	}
	if !allowCrossSession && !beneathAny(readonlyDirs, dir) {
		for _, base := range realAndLexical(runnerpaths.DataDir()) {
			if rel, err := filepath.Rel(base, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return "", fmt.Errorf("directory %q is not visible in the sandbox (the cross-session data directory is not mounted)", dir)
			}
		}
	}
	return dir, nil
}

// validateReadonlyRoots rejects operator readonly roots that cannot work in a
// sandbox: a root at or beneath a path the sandbox masks with a later tmpfs
// (it would be silently shadowed), or at or beneath the reserved /session or
// at /tmp itself (it would replace the chat's own bind).
func validateReadonlyRoots(readonlyDirs, masked []string) error {
	for _, d := range readonlyDirs {
		if !filepath.IsAbs(d) {
			continue // reported by appendReadonlyBinds
		}
		for _, c := range realAndLexical(d) {
			if c == "/tmp" || c == "/session" || strings.HasPrefix(c, "/session/") {
				return fmt.Errorf("mount readonly directory %q collides with the reserved sandbox mount point /tmp or /session", d)
			}
		}
		for _, m := range masked {
			if m != "" && beneathAny([]string{m}, d) {
				return fmt.Errorf("mount readonly directory %q lies inside %q, which the sandbox masks; it would be invisible", d, m)
			}
		}
	}
	return nil
}

// appendReadonlyBinds binds each dir read-only at its own path and,
// when it is a symlink, at its resolved path too: a process may reach the tree
// through either name and both must be read-only. A relative entry is an error
// (fail closed): it could not be mounted, leaving the tree writable.
func appendReadonlyBinds(args []string, dirs []string) ([]string, error) {
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("mount readonly directory must be an absolute path: %q", dir)
		}
		// A missing root has nothing to protect: skip it (loudly), the same
		// as agent sandboxes do.
		if _, err := os.Stat(dir); err != nil {
			log.Warn().Str("dir", dir).Msg("skipping missing readonly mount directory")
			continue
		}
		args = append(args, "--ro-bind", dir, dir)
		if ev, err := filepath.EvalSymlinks(dir); err == nil && ev != filepath.Clean(dir) {
			args = append(args, "--ro-bind", ev, ev)
		}
	}
	return args, nil
}

// appendBind binds dir read-write, or read-only when it is at or beneath one
// of readonlyDirs (compared by lexical and resolved path, so a symlink alias
// of a readonly tree cannot be mounted writable).
func appendBind(args []string, readonlyDirs []string, dir string) []string {
	if beneathAny(readonlyDirs, dir) {
		return append(args, "--ro-bind", dir, dir)
	}
	return append(args, "--bind", dir, dir)
}

// beneathAny reports whether p is one of dirs or lies beneath one.
func beneathAny(dirs []string, p string) bool {
	p = filepath.Clean(p)
	cands := realAndLexical(p)
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		for _, base := range realAndLexical(d) {
			for _, c := range cands {
				if rel, err := filepath.Rel(base, c); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
					return true
				}
			}
		}
	}
	return false
}

func realAndLexical(p string) []string {
	out := []string{filepath.Clean(p)}
	if ev, err := filepath.EvalSymlinks(p); err == nil && ev != out[0] {
		out = append(out, ev)
	}
	return out
}

// rwMountEntry is one read-write mount: a run directory or a
// mount_dirs.readwrite entry, with the subdir mask rule that applies to it.
type rwMountEntry struct {
	dir      string
	isRunDir bool
	rule     *agentspec.SubdirMaskRule
}

// mountPlanner plans the directory mounts shared by the agent and the command
// sandboxes: the glob expansion of run_dirs and mount_dirs, the read-write and
// read-only entries, the subdir mask rules and the late read-only binds. The
// two sandboxes differ only in the explicit settings below.
//
// Ordering contract (kept by the builders): read-write binds first, then the
// subdir mask rules (so a bind cannot shadow a mask), and the readonly roots
// plus worktree binds after every read-write bind so they win over any
// read-write ancestor.
type mountPlanner struct {
	cfg *agentspec.AgentConfig
	// readonly are the glob-expanded readonly roots; every read-write bind at
	// or beneath one is demoted to read-only.
	readonly []string
	// provided are directories another bind already makes visible (a command
	// sandbox binds HOME and the chat dirs wholesale): entries at or beneath
	// one are not bound again, and they count as mounted for the run dir. The
	// agent sandbox has none.
	provided []string
	// strict makes a missing run/mount directory an error (agent sandbox);
	// otherwise it is skipped (command sandbox).
	strict bool

	rw []rwMountEntry
}

func newMountPlanner(cfg *agentspec.AgentConfig, provided []string, strict bool, extraReadonly []string) *mountPlanner {
	p := &mountPlanner{cfg: cfg, provided: provided, strict: strict}
	p.readonly = append(p.readonly, expandReadonlyRoots(extraReadonly)...)
	if cfg != nil {
		p.readonly = append(p.readonly, expandReadonlyRoots(cfg.MountDirs.ReadOnly)...)
	}
	return p
}

// expandReadonlyRoots expands the glob entries of a readonly mount list and
// keeps every other entry as it is.
func expandReadonlyRoots(dirs []string) []string {
	var out []string
	for _, dir := range dirs {
		if expanded, isGlob := expandGlobDirs(dir); isGlob {
			out = append(out, expanded...)
		} else {
			out = append(out, dir)
		}
	}
	return out
}

// bindReadonlyRunDirs binds the run_dirs read-only (readonly lock mode). It
// returns the set of cleaned run dirs it saw (including skipped ones) and the
// list it actually bound.
func (p *mountPlanner) bindReadonlyRunDirs(args []string) ([]string, map[string]bool, []string, error) {
	seen := make(map[string]bool)
	var bound []string
	for _, dir := range p.cfg.RunDirs {
		cands, isGlob := expandGlobDirs(dir)
		if !isGlob {
			cands = []string{dir}
		}
		for _, m := range cands {
			clean := filepath.Clean(m)
			if seen[clean] {
				continue
			}
			seen[clean] = true
			if beneathAny(p.provided, m) {
				continue
			}
			if !isGlob {
				if _, err := os.Stat(m); err != nil {
					if p.strict {
						return nil, nil, nil, fmt.Errorf("run directory %q does not exist: %w", m, err)
					}
					continue
				}
			}
			args = append(args, "--ro-bind", m, m)
			bound = append(bound, m)
		}
	}
	return args, seen, bound, nil
}

// planReadWrite resolves run_dirs and mount_dirs.readwrite (glob expanded,
// deduplicated by cleaned path, run dirs first) into p.rw.
func (p *mountPlanner) planReadWrite() {
	cfg := p.cfg
	if cfg == nil {
		return
	}
	seen := make(map[string]bool)
	add := func(dir string, isRunDir bool, rule func(string) *agentspec.SubdirMaskRule) {
		cands, isGlob := expandGlobDirs(dir)
		if !isGlob {
			cands = []string{dir}
		}
		for _, m := range cands {
			clean := filepath.Clean(m)
			if seen[clean] {
				continue
			}
			seen[clean] = true
			p.rw = append(p.rw, rwMountEntry{dir: m, isRunDir: isRunDir, rule: rule(m)})
		}
	}
	for _, dir := range cfg.RunDirs {
		add(dir, true, cfg.MountDirs.MaskRuleFor)
	}
	for _, entry := range cfg.MountDirs.EffectiveReadWriteEntries() {
		add(entry.Path, false, func(m string) *agentspec.SubdirMaskRule {
			if entry.MaskRules != nil {
				return entry.MaskRules
			}
			return cfg.MountDirs.MaskRuleFor(m)
		})
	}
}

// bindReadWrite binds every planned read-write entry (read-only where it is
// beneath a readonly root) and returns the directories now mounted writable.
func (p *mountPlanner) bindReadWrite(args []string) ([]string, []string, error) {
	mounted := slices.Clone(p.provided)
	for _, entry := range p.rw {
		if beneathAny(p.provided, entry.dir) {
			continue
		}
		if _, err := os.Stat(entry.dir); err != nil {
			if !p.strict {
				continue
			}
			if entry.isRunDir {
				return nil, nil, fmt.Errorf("run directory %q does not exist: %w", entry.dir, err)
			}
			return nil, nil, fmt.Errorf("mount readwrite directory %q does not exist: %w", entry.dir, err)
		}
		args = appendBind(args, p.readonly, entry.dir)
		if !beneathAny(p.readonly, entry.dir) {
			mounted = append(mounted, entry.dir)
		}
	}
	return args, mounted, nil
}

// applyMaskRules applies the subdir mask rules of the planned entries. It must
// run after every read-write bind (including the run dir) so the masks are not
// shadowed.
func (p *mountPlanner) applyMaskRules(args []string, runDir string) ([]string, error) {
	for _, entry := range p.rw {
		if entry.rule != nil {
			var err error
			args, err = applySubdirMaskRule(args, entry.dir, entry.rule, runDir)
			if err != nil {
				return nil, err
			}
		}
	}
	return args, nil
}

// bindReadonlyLate binds the readonly roots and the chat's worktree after the
// read-write binds, so they win over any read-write ancestor.
func (p *mountPlanner) bindReadonlyLate(args []string) ([]string, error) {
	args, err := appendReadonlyBinds(args, p.readonly)
	if err != nil {
		return nil, err
	}
	return appendWorktreeArgs(args, p.cfg, p.readonly)
}

// appendCredentialMasks masks the credentials every sandbox hides: ~/.ssh
// (with only config, known_hosts and public keys visible), the GCP service
// account key, the Asgard config file and the proxy config/CA key. It must
// come after all directory binds so no later bind shadows a mask.
func appendCredentialMasks(args []string, home, configPath, caKey string) []string {
	args = appendSSHSandboxArgs(args, home)

	gcpSAPath := filepath.Join(home, ".config", "gcp-sa.json")
	if fi, err := os.Stat(gcpSAPath); err == nil && !fi.IsDir() {
		args = append(args, "--ro-bind", "/dev/null", gcpSAPath)
	}

	args = appendConfigMaskArgs(args, configPath)
	return appendProxySensitiveMaskArgs(args, caKey)
}
