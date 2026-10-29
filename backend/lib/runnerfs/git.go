package runnerfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Git safety. The runner executes git outside the agents' sandbox on a
// directory an agent can write to, so everything git would take from the
// repository is treated as hostile:
//
//   - The working directory is an fd opened beneath an allowed root
//     (/proc/self/fd/N), so a swapped path component cannot redirect git; the
//     repository root must lie beneath the same root as the run dir (the
//     search never climbs out of it) and its .git must be a real directory,
//     not a gitfile or symlink (no redirect to another repository).
//   - The repository-local config is read first (git config --local, which
//     executes nothing) and the repo is refused when it sets any key that makes
//     git run a program or read another config or tree (core.fsmonitor,
//     core.sshCommand, core.worktree, filter/diff/merge drivers, credential
//     helpers, includes, ...).
//   - Hooks are switched off (core.hooksPath=/dev/null), as are the fsmonitor
//     and external diff/textconv, on the command line, which outranks the
//     repo config. Repo-redirecting GIT_* variables are dropped from the
//     environment, prompts are disabled.
//   - Git is not given a pager, a tty or an editor.
//
// The vet is a second layer. Symlinks and hard links inside .git can only
// reach paths visible inside the git sandbox, where only /repo is mounted (and
// a hard link cannot cross mounts), so they cannot reach other chats' data;
// the walk refuses them anyway (any regular file with several links, except
// the immutable object and pack files of objects/ and modules/*/objects/,
// which a local clone shares with its source).
//
// Push runs with the whole repository read-only, readonly mounts included.
// Git then cannot update remote-tracking refs, so after a push that exited 0
// the runner writes the loose ref file of @{push} (resolved locally before the
// push, never taken from the push's output) with the commit that was pushed,
// through the confined write path with the readonly-mount veto.
//
// Operator-installed filters (for example git-lfs clean/smudge in the global
// gitconfig) still run when a repo's .gitattributes selects them, including
// during diff. That is accepted ONLY because the runner runs git in a bwrap
// sandbox (GitExec): diff, log and the config vet get the repository mounted
// read-only, no network (--unshare-net) and GIT_OPTIONAL_LOCKS=0, so such a
// filter can neither modify the work tree nor exfiltrate. Without a sandbox
// hook (development, tests) git runs directly and this does not hold.
//
// The system and global git config of the runner's user stay in effect (they
// are the operator's, not the agent's), as does git's own safe.directory
// ownership check; neither is widened. Residual risk: an agent that rewrites
// .git/config between the vet and the command can still race the check.

const (
	maxGitDirEntries  = 1_000_000
	maxGitDirDepth    = 32
	gitReadTimeout    = 2 * time.Minute
	gitNetworkTimeout = 10 * time.Minute
	gitSmallOut       = 1 << 20
	gitListOut        = 16 << 20
	// MaxGitError bounds the git output returned to the backend.
	MaxGitError = 64 << 10
)

var commitRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._/@^~{}:-]{0,199}$`)

// gitEnvDrop lists the variables that would redirect git to another repository
// or config.
var gitEnvDrop = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES", "GIT_EXTERNAL_DIFF", "GIT_PAGER",
	"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS",
	"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_PREFIX",
}

func gitEnv() []string {
	drop := map[string]bool{}
	for _, k := range gitEnvDrop {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] || strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")
}

// gitSafeArgs precede every git subcommand.
var gitSafeArgs = []string{
	"--no-pager",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "core.pager=cat",
	// Only network transports: file://, plain local paths and ext:: are
	// refused (their helper processes would bypass the hook and config
	// hardening). http(s), ssh and git are git's own defaults.
	"-c", "protocol.allow=never",
	"-c", "protocol.file.allow=never",
	"-c", "protocol.ext.allow=never",
	"-c", "protocol.https.allow=always",
	"-c", "protocol.http.allow=always",
	"-c", "protocol.ssh.allow=always",
	"-c", "protocol.git.allow=always",
	"-c", "color.ui=false",
	"-c", "core.quotePath=false",
}

// dangerousGitKey reports whether a repo-local config key (lowercased) lets
// the repository run a program, read other config or redirect the work tree.
func dangerousGitKey(k string) bool {
	switch k {
	case "core.fsmonitor", "core.hookspath", "core.sshcommand", "core.pager", "core.editor", "core.askpass",
		"core.gitproxy", "core.worktree", "core.alternaterefscommand", "core.gitdir", "diff.external", "diff.tool",
		"merge.tool", "gpg.program", "gpg.openpgp.program", "gpg.x509.program", "gpg.ssh.program",
		"sequence.editor", "credential.helper", "uploadpack.packobjectshook", "extensions.worktreeconfig",
		"include.path":
		return true
	}
	switch {
	case strings.HasPrefix(k, "include."), strings.HasPrefix(k, "includeif."):
		return true
	case strings.HasPrefix(k, "protocol."):
		// A specific repo-local protocol.<name>.allow would beat the
		// generic command-line setting.
		return true
	case strings.HasPrefix(k, "filter."), strings.HasPrefix(k, "mergetool."), strings.HasPrefix(k, "difftool."):
		return true
	case strings.HasPrefix(k, "credential.") && strings.HasSuffix(k, ".helper"):
		return true
	case strings.HasPrefix(k, "diff.") && (strings.HasSuffix(k, ".command") || strings.HasSuffix(k, ".textconv")):
		return true
	case strings.HasPrefix(k, "merge.") && strings.HasSuffix(k, ".driver"):
		return true
	case strings.HasPrefix(k, "remote.") && (strings.HasSuffix(k, ".uploadpack") || strings.HasSuffix(k, ".receivepack") || strings.HasSuffix(k, ".vcs")):
		return true
	case strings.HasPrefix(k, "submodule.") && strings.HasSuffix(k, ".update"):
		return true
	}
	return false
}

// capWriter keeps the first max bytes and fails the writer (ending the child
// with EPIPE) once more arrive, so a huge blob does not run to completion.
type capWriter struct {
	max  int
	buf  bytes.Buffer
	over bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.over = true
		return room, io.ErrShortWrite
	}
	return c.buf.Write(p)
}

// GitExecSpec describes one git invocation for a GitExec hook.
type GitExecSpec struct {
	ChatID string
	// Git are the arguments after the git binary, already pinned to the
	// repository (--git-dir/--work-tree) and hardened.
	Git []string
	// Env are KEY=VALUE variables git needs (the sandbox clears the rest).
	Env []string
	// WorkTree is the repository directory; the hook must pass it as the
	// command's first extra file (fd 3) and mount it at SandboxRepoDir.
	WorkTree  *os.File
	ReadWrite bool
	Network   bool
}

// SandboxRepoDir is where a GitExec hook mounts the repository.
const SandboxRepoDir = "/repo"

// GitExecFunc builds the command that runs git. The runner supplies a hook
// that runs git inside a bubblewrap sandbox holding only the repository; with
// no hook (development, tests) git runs directly.
type GitExecFunc func(ctx context.Context, spec GitExecSpec) (*exec.Cmd, error)

// gitRepo is an opened repository root.
type gitRepo struct {
	fd   int
	Root string
	dir  string // /proc/self/fd/<fd>, the parent's name for the work tree
	l    Local
	// rootFile/gitFile own the work tree and .git directory fds; git gets
	// them as inherited fds 3 and 4 (--work-tree/--git-dir).
	rootFile *os.File
	gitFile  *os.File
	rw       bool // repository mounted writable (pull)
	// remoteRefs are the remote names/URLs the repo config selects (vetConfig).
	remoteRefs []string
	// knownRemotes are the configured remote names (checkRemote).
	knownRemotes map[string]bool
	network      bool // the command may use the network (push/pull)
}

func (r *gitRepo) close() {
	if r.rootFile != nil {
		_ = r.rootFile.Close()
	} else {
		_ = unix.Close(r.fd)
	}
	if r.gitFile != nil {
		_ = r.gitFile.Close()
	}
}

// gitStaticEnv is the environment git needs inside the sandbox.
func (r *gitRepo) staticEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no", "GIT_DISCOVERY_ACROSS_FILESYSTEM=0"}
	if !r.rw {
		env = append(env, "GIT_OPTIONAL_LOCKS=0")
	}
	return env
}

// command builds a git command pinned to the vetted work tree and git dir.
// With a GitExec hook it runs in the sandbox, which sees only the work tree
// (at /repo); without one, git gets the directories as inherited fds.
func (r *gitRepo) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if r.l.GitExec != nil {
		pin := []string{"--git-dir=" + SandboxRepoDir + "/.git", "--work-tree=" + SandboxRepoDir}
		all := append(append(append([]string{}, gitSafeArgs...), pin...), args...)
		return r.l.GitExec(ctx, GitExecSpec{
			ChatID: r.l.ChatID, Git: all, Env: r.staticEnv(), WorkTree: r.rootFile, ReadWrite: r.rw, Network: r.network,
		})
	}
	pin := []string{"--git-dir=/proc/self/fd/3", "--work-tree=/proc/self/fd/4"}
	all := append(append(append([]string{}, gitSafeArgs...), pin...), args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	cmd.Dir = r.dir
	cmd.ExtraFiles = []*os.File{r.gitFile, r.rootFile}
	cmd.Env = append(gitEnv(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=0", "GIT_CEILING_DIRECTORIES="+filepath.Dir(r.Root))
	if !r.rw {
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	}
	return cmd, nil
}

// vetGitDir refuses a .git directory that points git elsewhere: a commondir
// or gitdir redirect, alternate object stores, or a symlink among the entries
// git reads or writes (objects and refs are walked one level deep per
// directory level that matters).
func vetGitDir(gfd int) error {
	deny := func(what string) error {
		return Errf(CodeDenied, "access denied: .git %s would leave the repository", what)
	}
	exists := func(dirfd int, name string) bool {
		var st unix.Stat_t
		return unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
	}
	for _, name := range []string{"commondir", "gitdir"} {
		if exists(gfd, name) {
			return deny(name)
		}
	}
	for _, name := range []string{"HEAD", "config", "config.worktree", "index", "packed-refs", "shallow", "objects", "refs", "info", "logs", "hooks", "modules", "worktrees"} {
		var st unix.Stat_t
		if unix.Fstatat(gfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return deny("symlink " + name)
		}
	}
	for _, rel := range []string{"objects/info/alternates", "objects/info/http-alternates"} {
		if exists(gfd, rel) {
			return deny(rel)
		}
	}
	// No symlink anywhere under .git (bounded walk; exceeding it refuses).
	entries := 0
	var walk func(dirfd int, depth int, rel string) error
	walk = func(dirfd int, depth int, rel string) error {
		names, err := readDirNames(dirfd)
		if err != nil {
			return err
		}
		for _, n := range names {
			if entries++; entries > maxGitDirEntries {
				return Errf(CodeDenied, "access denied: .git is too large to vet")
			}
			var st unix.Stat_t
			if unix.Fstatat(dirfd, n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFLNK:
				return deny("symlink " + n)
			case unix.S_IFREG:
				if st.Nlink > 1 && !gitImmutableFile(path.Join(rel, n)) {
					return deny("hard-linked file " + n)
				}
			case unix.S_IFDIR:
				if depth >= maxGitDirDepth {
					return Errf(CodeDenied, "access denied: .git is nested too deeply to vet")
				}
				cfd, err := unix.Openat(dirfd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return deny("unreadable entry " + n)
				}
				werr := walk(cfd, depth+1, path.Join(rel, n))
				_ = unix.Close(cfd)
				if werr != nil {
					return werr
				}
			}
		}
		return nil
	}
	wfd, err := unix.Dup(gfd)
	if err != nil {
		return mapErrno(err)
	}
	defer func() { _ = unix.Close(wfd) }()
	return walk(wfd, 0, "")
}

// run executes git in the repo root and returns stdout capped at max bytes
// (over reports a cut). A nonzero exit is returned as the error.
func (r *gitRepo) run(ctx context.Context, timeout time.Duration, max int, args ...string) (out []byte, over bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := r.command(ctx, args...)
	if err != nil {
		return nil, false, Errf(CodeInternal, "cannot start git: %v", err)
	}
	var stderr capWriter
	stderr.max = 64 << 10
	cw := &capWriter{max: max}
	cmd.Stdout, cmd.Stderr = cw, &stderr
	err = cmd.Run()
	if cw.over {
		return cw.buf.Bytes(), true, nil
	}
	if err != nil && stderr.buf.Len() > 0 && !errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.buf.String()))
	}
	return cw.buf.Bytes(), false, err
}

// gitRootPath returns the git root enclosing dir ("" if none), searching only
// beneath the allowed roots. A gitfile counts (the path is only reported).
func (l Local) gitRootPath(dir string, roots []string) string {
	fd, root, _, err := l.findGitRoot(dir, roots)
	if err != nil {
		return ""
	}
	_ = unix.Close(fd)
	return root
}

// findGitRoot walks up from start, reopening every ancestor beneath the
// allowed roots, and returns the first directory holding a .git entry with its
// open fd. The first open's error is returned as is; running out of allowed
// roots (or the filesystem) is CodeNotFound.
func (l Local) findGitRoot(start string, roots []string) (fd int, root string, gitIsDir bool, err error) {
	cur := filepath.Clean(start)
	for first := true; ; first = false {
		fd, oerr := l.openDirAbs(roots, cur)
		if oerr != nil {
			if first {
				return -1, "", false, oerr
			}
			break
		}
		var st unix.Stat_t
		if unix.Fstatat(fd, ".git", &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			return fd, cur, st.Mode&unix.S_IFMT == unix.S_IFDIR, nil
		}
		_ = unix.Close(fd)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return -1, "", false, Errf(CodeNotFound, "directory is not inside a git repository")
}

// openRepo opens and vets the repository enclosing the chat's run dir.
func (l Local) openRepo(ctx context.Context, runDir string, write bool) (*gitRepo, error) {
	if err := l.checkChat(); err != nil {
		return nil, err
	}
	wsfd, ws, err := l.openWorkspace(runDir, write)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil, Errf(CodeBadRequest, "directory does not exist")
		}
		return nil, err
	}
	_ = unix.Close(wsfd)
	fd, root, isDir, err := l.findGitRoot(ws, l.roots(write, ""))
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil, Errf(CodeBadRequest, "directory is not inside a git repository")
		}
		return nil, err
	}
	r := &gitRepo{fd: fd, Root: root, dir: "/proc/self/fd/" + strconv.Itoa(fd), l: l, rw: write, rootFile: os.NewFile(uintptr(fd), "worktree")}
	fail := func(err error) (*gitRepo, error) { r.close(); return nil, err }
	if !isDir {
		return fail(Errf(CodeDenied, "access denied: .git is not a directory"))
	}
	gfd, err := unix.Openat(fd, ".git", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(mapErrno(err))
	}
	r.gitFile = os.NewFile(uintptr(gfd), "gitdir")
	if err := vetGitDir(gfd); err != nil {
		return fail(err)
	}
	if write {
		if err := l.guard().denyDirFd(fd); err != nil {
			return fail(err)
		}
	}
	if err := r.vetConfig(ctx); err != nil {
		return fail(err)
	}
	return r, nil
}

// vetConfig refuses a repository whose local config can make git execute
// programs or leave the repository.
// vetConfigCap bounds the config listing the vet reads (a var for tests).
var vetConfigCap = gitListOut

func (r *gitRepo) vetConfig(ctx context.Context) error {
	out, over, err := r.run(ctx, gitReadTimeout, vetConfigCap, "config", "--local", "--no-includes", "--list", "-z")
	if err != nil || over {
		// Fail closed: a config that cannot be listed in full is not vetted.
		return Errf(CodeDenied, "access denied: repository config could not be vetted")
	}
	for _, kv := range bytes.Split(out, []byte{0}) {
		key, val, _ := bytes.Cut(kv, []byte{'\n'})
		k := strings.ToLower(string(key))
		if dangerousGitKey(k) {
			return Errf(CodeDenied, "access denied: repository config %q is not allowed on the runner", k)
		}
		// Values naming the remote push/pull will use; git treats a name that
		// is not a configured remote as a URL or path.
		if k == "remote.pushdefault" || strings.HasPrefix(k, "branch.") && (strings.HasSuffix(k, ".remote") || strings.HasSuffix(k, ".pushremote")) {
			r.remoteRefs = append(r.remoteRefs, string(val))
		}
	}
	return nil
}

// capString cuts b at max bytes on a rune boundary and makes it valid UTF-8
// (protobuf strings must be).
func capString(b []byte, max int) (string, bool) {
	cut := false
	if len(b) > max {
		b, cut = b[:max], true
		// Drop a rune split by the cut.
		for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
			if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
				break
			}
			b = b[:len(b)-1]
		}
	}
	return strings.ToValidUTF8(string(b), "�"), cut
}

// gitText returns stdout of a git command as capped text; failures yield "".
func (r *gitRepo) gitText(ctx context.Context, max int, args ...string) (string, bool) {
	out, over, err := r.run(ctx, gitReadTimeout, max+1, args...)
	if err != nil {
		return "", false
	}
	if over || len(out) > max {
		s, _ := capString(out, max)
		return s, true
	}
	s, _ := capString(out, max)
	return s, false
}

type nameStatusEntry struct {
	status  string
	oldPath string
	newPath string
}

// parseNameStatus parses `git ... --name-status -z` output: NUL-separated
// tokens, a status followed by one path (two for renames and copies). Paths
// may contain any byte except NUL.
func parseNameStatus(output string) []nameStatusEntry {
	var entries []nameStatusEntry
	toks := strings.Split(output, "\x00")
	for i := 0; i < len(toks); {
		st := toks[i]
		i++
		if st == "" || !strings.ContainsRune("AMDRCTUX", rune(st[0])) {
			continue // separator noise (show --pretty=format: with -z)
		}
		n := 1
		if st[0] == 'R' || st[0] == 'C' {
			n = 2
		}
		if i+n > len(toks) {
			break
		}
		if n == 2 {
			entries = append(entries, nameStatusEntry{status: st[:1], oldPath: toks[i], newPath: toks[i+1]})
		} else {
			entries = append(entries, nameStatusEntry{status: st[:1], oldPath: toks[i], newPath: toks[i]})
		}
		i += n
	}
	return entries
}

// mergeNameStatusEntries merges by newPath, preferring entries from a.
func mergeNameStatusEntries(a, b []nameStatusEntry) []nameStatusEntry {
	merged := make([]nameStatusEntry, 0, len(a)+len(b))
	seen := make(map[string]int, len(a)+len(b))
	for _, e := range b {
		seen[e.newPath] = len(merged)
		merged = append(merged, e)
	}
	for _, e := range a {
		if idx, ok := seen[e.newPath]; ok {
			merged[idx] = e
		} else {
			seen[e.newPath] = len(merged)
			merged = append(merged, e)
		}
	}
	return merged
}

// readWorktree reads a working-tree file beneath the repo root without
// following any symlink; missing or irregular files read as "".
func (r *gitRepo) readWorktree(rel string) (string, bool) {
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", false
	}
	fd, err := openResolve(r.fd, rel, unix.O_RDONLY|unix.O_NONBLOCK, 0, strictFlags)
	if err != nil {
		return "", false
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	_ = unix.SetNonblock(fd, false)
	b, _ := io.ReadAll(io.LimitReader(f, MaxDiffSide+1))
	return capString(b, MaxDiffSide)
}

// finish trims f so that the frame stays under MaxDiffFrame.
func (f *GitDiffFile) finish() {
	sum := len(f.OldPath) + len(f.NewPath) + len(f.OldContent) + len(f.NewContent)
	for _, h := range f.Hunks {
		sum += len(h)
	}
	over := sum - MaxDiffFrame
	if over <= 0 {
		return
	}
	for i := len(f.Hunks) - 1; i >= 0 && over > 0; i-- {
		cut := min(over, len(f.Hunks[i]))
		f.Hunks[i], _ = capString([]byte(f.Hunks[i]), len(f.Hunks[i])-cut)
		over -= cut
		f.HunksTruncated = true
	}
	for _, s := range []*string{&f.NewContent, &f.OldContent} {
		if over <= 0 {
			break
		}
		cut := min(over, len(*s))
		*s, _ = capString([]byte(*s), len(*s)-cut)
		over -= cut
		f.ContentTruncated = true
	}
}

func (r *gitRepo) hunkOf(ctx context.Context, args ...string) ([]string, bool) {
	out, over, err := r.run(ctx, gitReadTimeout, MaxDiffHunks+1, args...)
	if err != nil && len(out) == 0 {
		return nil, false
	}
	if len(out) == 0 {
		return nil, false
	}
	s, cut := capString(out, MaxDiffHunks)
	return []string{s}, cut || over
}

// GitDiff implements DirOps.
func (l Local) GitDiff(ctx context.Context, req GitDiffRequest, yield func(GitDiffFile) bool) error {
	r, err := l.openRepo(ctx, req.RunDir, false)
	if err != nil {
		return err
	}
	defer r.close()

	commit := strings.TrimSpace(req.Commit)
	if commit != "" && commit != "unstash" && commit != "working" {
		return r.commitDiff(ctx, commit, yield)
	}

	nameStatus, _, err := r.run(ctx, gitReadTimeout, gitListOut, "diff", "HEAD", "--name-status", "-z", "--no-ext-diff")
	if err != nil {
		// Fallback for repos with no commits yet.
		nameStatus, _, err = r.run(ctx, gitReadTimeout, gitListOut, "diff", "--name-status", "-z", "--no-ext-diff")
		if err != nil {
			return Errf(CodeInternal, "git diff failed")
		}
	}
	staged, _, _ := r.run(ctx, gitReadTimeout, gitListOut, "diff", "--cached", "--name-status", "-z", "--no-ext-diff")
	merged := mergeNameStatusEntries(parseNameStatus(string(nameStatus)), parseNameStatus(string(staged)))

	untracked, _, _ := r.run(ctx, gitReadTimeout, gitListOut, "ls-files", "--others", "--exclude-standard", "-z")

	for _, e := range merged {
		if ctx.Err() != nil {
			return Errf(CodeUnavailable, "request canceled")
		}
		f := r.trackedEntry(ctx, e)
		if !yield(f) {
			return nil
		}
	}
	for _, rel := range strings.Split(string(untracked), "\x00") {
		if rel == "" {
			continue
		}
		if ctx.Err() != nil {
			return Errf(CodeUnavailable, "request canceled")
		}
		f, ok := r.untrackedEntry(ctx, rel)
		if !ok {
			continue
		}
		if !yield(f) {
			return nil
		}
	}
	return nil
}

func (r *gitRepo) trackedEntry(ctx context.Context, e nameStatusEntry) GitDiffFile {
	f := GitDiffFile{OldPath: e.oldPath, NewPath: e.newPath, Status: e.status, Hunks: []string{}}
	old, oldCut := r.gitText(ctx, MaxDiffSide, "show", "HEAD:"+e.oldPath)
	cur, curCut := r.readWorktree(e.newPath)
	f.OldContent, f.NewContent, f.ContentTruncated = old, cur, oldCut || curCut
	// One element so the diff viewer sees the full diff including headers.
	h, cut := r.hunkOf(ctx, "diff", "HEAD", "--no-ext-diff", "--no-textconv", "--", e.newPath)
	if h == nil {
		h, cut = r.hunkOf(ctx, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--", e.newPath)
	}
	if h != nil {
		f.Hunks, f.HunksTruncated = h, cut
	}
	f.finish()
	return f
}

func (r *gitRepo) untrackedEntry(ctx context.Context, rel string) (GitDiffFile, bool) {
	cur, cut := r.readWorktree(rel)
	if cur == "" {
		// Unreadable, empty or irregular: skipped like a read error was.
		if _, ok := r.statRegular(rel); !ok {
			return GitDiffFile{}, false
		}
	}
	f := GitDiffFile{OldPath: "/dev/null", NewPath: rel, Status: "A", NewContent: cur, ContentTruncated: cut, Hunks: []string{}}
	// --no-index exits 1 when files differ, which a new file always does.
	out, over, _ := r.run(ctx, gitReadTimeout, MaxDiffHunks+1, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--", "/dev/null", rel)
	if len(out) > 0 {
		s, c := capString(out, MaxDiffHunks)
		f.Hunks, f.HunksTruncated = []string{s}, c || over
	}
	f.finish()
	return f, true
}

func (r *gitRepo) statRegular(rel string) (os.FileInfo, bool) {
	fd, err := openResolve(r.fd, path.Clean(rel), unix.O_RDONLY|unix.O_NONBLOCK, 0, strictFlags)
	if err != nil {
		return nil, false
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	return info, err == nil && info.Mode().IsRegular()
}

// commitDiff yields the files a commit changed. The commit is validated and
// resolved to a hash first, so it cannot act as an option.
func (r *gitRepo) commitDiff(ctx context.Context, commit string, yield func(GitDiffFile) bool) error {
	if !commitRe.MatchString(commit) {
		return Errf(CodeBadRequest, "invalid commit %q", commit)
	}
	out, _, err := r.run(ctx, gitReadTimeout, 4096, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{commit}")
	hash := strings.TrimSpace(string(out))
	if err != nil || hash == "" {
		return Errf(CodeInternal, "failed to get commit diff: unknown commit %q", commit)
	}
	nameStatus, _, err := r.run(ctx, gitReadTimeout, gitListOut, "diff-tree", "--no-commit-id", "--name-status", "-z", "-r", "--no-ext-diff", hash)
	if err != nil {
		nameStatus, _, err = r.run(ctx, gitReadTimeout, gitListOut, "show", "--name-status", "-z", "--pretty=format:", "--no-ext-diff", hash)
		if err != nil {
			return Errf(CodeInternal, "failed to get commit diff: %v", err)
		}
	}
	for _, e := range parseNameStatus(string(nameStatus)) {
		if ctx.Err() != nil {
			return Errf(CodeUnavailable, "request canceled")
		}
		f := GitDiffFile{OldPath: e.oldPath, NewPath: e.newPath, Status: e.status, Hunks: []string{}}
		old, oldCut := r.gitText(ctx, MaxDiffSide, "show", hash+"~1:"+e.oldPath)
		cur, curCut := r.gitText(ctx, MaxDiffSide, "show", hash+":"+e.newPath)
		f.OldContent, f.NewContent, f.ContentTruncated = old, cur, oldCut || curCut
		out, over, derr := r.run(ctx, gitReadTimeout, MaxDiffHunks+1, "show", "--pretty=format:", "--patch", "--no-ext-diff", "--no-textconv", hash, "--", e.newPath)
		if (derr == nil || over) && len(bytes.TrimSpace(out)) > 0 {
			s, c := capString(bytes.TrimSpace(out), MaxDiffHunks)
			f.Hunks, f.HunksTruncated = []string{s}, c || over
		}
		f.finish()
		if !yield(f) {
			return nil
		}
	}
	return nil
}

// GitLog implements DirOps.
func (l Local) GitLog(ctx context.Context, runDir string, limit int) (GitLogResult, error) {
	r, err := l.openRepo(ctx, runDir, false)
	if err != nil {
		return GitLogResult{}, err
	}
	defer r.close()
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	res := GitLogResult{Commits: []GitCommit{}}
	logOut, _ := r.gitText(ctx, gitSmallOut, "log", fmt.Sprintf("-n%d", limit), "--no-ext-diff", "--pretty=format:%H%x00%h%x00%an%x00%ae%x00%aI%x00%ar%x00%D%x00%s")
	if raw := strings.TrimSpace(logOut); raw != "" {
		for _, line := range strings.Split(raw, "\n") {
			p := strings.Split(line, "\x00")
			if len(p) >= 8 {
				res.Commits = append(res.Commits, GitCommit{
					Hash: p[0], ShortHash: p[1], Author: p[2], AuthorEmail: p[3],
					Date: p[4], RelativeDate: p[5], Refs: p[6], Message: p[7],
				})
			}
		}
	}
	branch, _ := r.gitText(ctx, 4096, "branch", "--show-current")
	res.CurrentBranch = strings.TrimSpace(branch)
	if res.CurrentBranch == "" {
		if rev, _ := r.gitText(ctx, 4096, "rev-parse", "--short", "HEAD"); strings.TrimSpace(rev) != "" {
			res.CurrentBranch = "HEAD (" + strings.TrimSpace(rev) + ")"
		}
	}
	tracking, _ := r.gitText(ctx, 4096, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	res.TrackingBranch = strings.TrimSpace(tracking)
	if res.TrackingBranch != "" {
		if counts, _ := r.gitText(ctx, 4096, "rev-list", "--left-right", "--count", "HEAD...@{u}"); counts != "" {
			if f := strings.Fields(counts); len(f) >= 2 {
				res.Ahead, _ = strconv.Atoi(f[0])
				res.Behind, _ = strconv.Atoi(f[1])
			}
		}
	}
	status, _ := r.gitText(ctx, gitListOut, "status", "--porcelain")
	for _, ln := range strings.Split(strings.TrimSpace(status), "\n") {
		if strings.TrimSpace(ln) != "" {
			res.UnstashedCount++
		}
	}
	return res, nil
}

func (l Local) gitNetwork(ctx context.Context, runDir, sub string) (GitOpResult, error) {
	// pull rewrites the work tree, so it needs a writable root; push does not.
	r, err := l.openRepo(ctx, runDir, sub == "pull")
	if err != nil {
		return GitOpResult{}, err
	}
	defer r.close()
	cw := &capWriter{max: MaxGitError * 2}
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	r.network = true
	hasRemote, err := r.checkRemote(ctx)
	if err != nil {
		return GitOpResult{}, err
	}
	if !hasRemote {
		// With no remote git would take "origin" for a relative path.
		return GitOpResult{Error: "no remote is configured"}, nil
	}
	// Resolve what a push will update before running it; success is git's
	// exit code and nothing else.
	var trackRef, trackHash string
	var trackOK bool
	if sub == "push" {
		trackRef, trackHash, trackOK = r.pushTracking(ctx)
	}
	cmd, err := r.command(ctx, sub)
	if err != nil {
		return GitOpResult{}, Errf(CodeInternal, "cannot start git: %v", err)
	}
	cmd.Stdout, cmd.Stderr = cw, cw
	runErr := cmd.Run()
	out, _ := capString(cw.buf.Bytes(), MaxGitError*2)
	if sub == "push" && trackOK {
		// The repository is read-only for git, so it could not update the
		// remote-tracking ref itself: record it here after a successful push.
		out = stripTrackingErrors(out)
		if runErr == nil {
			r.writeTracking(trackRef, trackHash)
		}
	}
	if runErr != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = runErr.Error()
		}
		return GitOpResult{Output: out, Error: msg}, nil
	}
	return GitOpResult{Success: true, Output: strings.TrimSpace(out)}, nil
}

// GitPush implements DirOps. The request id is honored by the runner service.
func (l Local) GitPush(ctx context.Context, runDir, _ string) (GitOpResult, error) {
	return l.gitNetwork(ctx, runDir, "push")
}

// GitPull implements DirOps.
func (l Local) GitPull(ctx context.Context, runDir, _ string) (GitOpResult, error) {
	return l.gitNetwork(ctx, runDir, "pull")
}

// remoteURLAllowed reports whether a resolved remote URL is a network URL:
// https, http, ssh, git or scp-like host:path. Plain paths, file:, ext:: and
// option-like strings are not (a bare word is a relative path to git unless it
// names a configured remote, which callers resolve first).
func remoteURLAllowed(u string) bool {
	if u == "" || strings.HasPrefix(u, "-") {
		return false
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(strings.ToLower(u), scheme) {
			return true
		}
	}
	// scp-like host:path, where no slash precedes the colon.
	if i := strings.IndexByte(u, ':'); i > 0 && !strings.ContainsAny(u[:i], "/\\") {
		return !strings.HasPrefix(u[i:], "::") && !strings.EqualFold(u[:i], "file")
	}
	return false
}

// checkRemote validates every URL git could use for push/pull before it runs:
// the fetch and push URLs of each configured remote (after insteadOf and
// pushInsteadOf), and any other remote the repo config selects, which git
// would take for a URL or path. Local paths, file:// and ext:: are refused;
// the command-line protocol.allow=never is the enforcement behind this.
func (r *gitRepo) checkRemote(ctx context.Context) (hasRemote bool, err error) {
	out, _, lerr := r.run(ctx, gitReadTimeout, 1<<20, "remote")
	if lerr != nil {
		return false, nil // git reports its own failure on the real command
	}
	known := map[string]bool{}
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n != "" {
			known[n] = true
		}
	}
	r.knownRemotes = known
	deny := func(what, u string) error {
		return Errf(CodeDenied, "access denied: %s %q is not a network URL", what, u)
	}
	for n := range known {
		for _, push := range []bool{false, true} {
			args := []string{"remote", "get-url", "--all"}
			if push {
				args = append(args, "--push")
			}
			urls, _, uerr := r.run(ctx, gitReadTimeout, 1<<20, append(args, "--", n)...)
			if uerr != nil {
				return true, Errf(CodeDenied, "access denied: remote %q could not be resolved", n)
			}
			for _, u := range strings.Split(strings.TrimSpace(string(urls)), "\n") {
				if !remoteURLAllowed(strings.TrimSpace(u)) {
					return true, deny("remote "+n+" url", strings.TrimSpace(u))
				}
			}
		}
	}
	for _, ref := range r.remoteRefs {
		if ref == "" || known[ref] {
			continue
		}
		// Not a configured remote: git would treat it as a URL or path.
		u := ref
		if res, _, gerr := r.run(ctx, gitReadTimeout, 4096, "ls-remote", "--get-url", "--", ref); gerr == nil {
			u = strings.TrimSpace(string(res))
		}
		if !remoteURLAllowed(u) {
			return len(known) > 0, deny("configured remote", ref)
		}
	}
	return len(known) > 0, nil
}

// refSafe reports whether s is a conservative ref-name component path: no
// "..", no empty, dotted or lock-suffixed components, only [A-Za-z0-9._/-].
func refSafe(s string) bool {
	if s == "" || len(s) > 200 || strings.Contains(s, "..") || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return false
	}
	for _, comp := range strings.Split(s, "/") {
		if comp == "" || strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") || strings.HasSuffix(comp, ".") {
			return false
		}
		for _, c := range comp {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

var hexHash = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// pushTracking resolves, locally and without the network, the remote-tracking
// ref a plain `git push` of the current branch updates (@{push}) and the
// commit being pushed. ok is false when git cannot say (push.default nothing,
// no upstream, detached HEAD, ...) or the answer is not a safe
// refs/remotes/<configured remote>/<branch> name. Nothing from the push's own
// output (which a server can influence) is used.
func (r *gitRepo) pushTracking(ctx context.Context) (ref, hash string, ok bool) {
	out, _, err := r.run(ctx, gitReadTimeout, 4096, "rev-parse", "--symbolic-full-name", "--end-of-options", "@{push}")
	if err == nil {
		ref = strings.TrimSpace(string(out))
	} else {
		// @{push} needs the tracking ref to exist; before the first push work
		// the destination out of the (local) configuration.
		ref = r.configuredPushRef(ctx)
	}
	if !r.trackingRefOK(ref) {
		return "", "", false
	}
	res, _, err := r.run(ctx, gitReadTimeout, 4096, "rev-parse", "--verify", "--quiet", "--end-of-options", "HEAD^{commit}")
	hash = strings.TrimSpace(string(res))
	if err != nil || !hexHash.MatchString(hash) {
		return "", "", false
	}
	return ref, hash, true
}

// trackingRefOK reports whether ref is refs/remotes/<configured remote>/<branch>
// with safe names.
func (r *gitRepo) trackingRefOK(ref string) bool {
	rest, found := strings.CutPrefix(ref, "refs/remotes/")
	if !found || !refSafe(ref) {
		return false
	}
	for n := range r.knownRemotes {
		if b, has := strings.CutPrefix(rest, n+"/"); has && b != "" && refSafe(n) && refSafe(b) {
			return true
		}
	}
	return false
}

// configuredPushRef derives the tracking ref of a plain `git push` of the
// current branch from local config: push.default simple/current push to the
// same branch name, upstream to the configured merge branch; any other mode,
// a fetch refspec list other than the single standard one, an ambiguous merge branch, or any remote.<name>.push refspec, yields "".
func (r *gitRepo) configuredPushRef(ctx context.Context) string {
	get := func(args ...string) string {
		out, _, err := r.run(ctx, gitReadTimeout, 4096, append([]string{"config", "--get"}, args...)...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	cur, _, err := r.run(ctx, gitReadTimeout, 4096, "branch", "--show-current")
	b := strings.TrimSpace(string(cur))
	if err != nil || !refSafe(b) {
		return ""
	}
	remote := get("branch." + b + ".pushremote")
	if remote == "" {
		remote = get("remote.pushdefault")
	}
	if remote == "" {
		remote = get("branch." + b + ".remote")
	}
	if !r.knownRemotes[remote] || !refSafe(remote) {
		return ""
	}
	// remote.<name>.push refspecs override push.default (mappings, wildcards,
	// deletions): what a push updates is then not derivable here.
	if pushSpecs, _, perr := r.run(ctx, gitReadTimeout, 4096, "config", "--get-all", "remote."+remote+".push"); perr == nil && strings.TrimSpace(string(pushSpecs)) != "" {
		return ""
	}
	dest := ""
	switch get("push.default") {
	case "", "simple", "current":
		dest = b
	case "upstream":
		// Git uses the first value; accept only an unambiguous one.
		merges, _, merr := r.run(ctx, gitReadTimeout, 4096, "config", "--get-all", "branch."+b+".merge")
		if vals := nonEmptyLines(string(merges)); merr == nil && len(vals) == 1 {
			dest, _ = strings.CutPrefix(vals[0], "refs/heads/")
		}
	}
	if dest == "" {
		return ""
	}
	fetch, _, err := r.run(ctx, gitReadTimeout, 4096, "config", "--get-all", "remote."+remote+".fetch")
	if err != nil {
		return ""
	}
	// Git maps through the first matching fetch refspec: accept exactly one,
	// the standard one.
	std := "refs/heads/*:refs/remotes/" + remote + "/*"
	if vals := nonEmptyLines(string(fetch)); len(vals) != 1 || strings.TrimPrefix(vals[0], "+") != std {
		return ""
	}
	return "refs/remotes/" + remote + "/" + dest
}

// writeTracking writes the loose remote-tracking ref through the confined
// write path (beneath the repo root, no symlink followed, atomic, readonly
// mount veto: a vetoed or failed write leaves the ref stale). No reflog.
func (r *gitRepo) writeTracking(ref, hash string) {
	_, _ = writeBeneath(r.fd, path.Join(".git", ref), WriteRequest{Mkdir: true, Atomic: true}, 0o644,
		strings.NewReader(hash+"\n"), r.l.guard())
}

// stripTrackingErrors drops git's expected complaints about not being able to
// update remote-tracking refs in the read-only repository.
func stripTrackingErrors(out string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "update_ref failed for ref 'refs/remotes/") || strings.Contains(line, "cannot lock ref 'refs/remotes/") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// gitImmutableFile reports whether rel (relative to .git) is an object or
// pack file, which git never modifies once written: hard links there (a local
// clone shares them with its source) are fine, anywhere else they are not.
func gitImmutableFile(rel string) bool {
	if strings.HasPrefix(rel, "objects/") {
		return true
	}
	return strings.HasPrefix(rel, "modules/") && strings.Contains(rel, "/objects/")
}

// nonEmptyLines splits s into its trimmed, non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
