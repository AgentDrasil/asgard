// Package worktree manages the per-chat git worktrees of agents that opted in
// to worktree isolation. The runner creates the worktree before the agent
// starts and removes it afterwards; the agent only ever works inside it (see
// bwrap.appendWorktreeArgs for the sandbox view) and brings its commits back
// to the main checkout through Merge.
//
// The git commands run on the runner outside the agents' sandboxes, on a
// repository other chats may write, so they carry the same hardening as the
// runner's file-system git API: hooks and external programs off, redirecting
// environment dropped, and a repository whose local config can run programs
// is refused.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

const (
	// BranchPrefix owns the branches of worktree chats: <prefix>/<chat-id>/<name>.
	BranchPrefix = "asgard"
	// BranchName is the branch the worktree starts on.
	BranchName = "work"

	gitTimeout = 2 * time.Minute
)

var (
	chatIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// ErrNotGitRepo reports that the directory is not the root of a git checkout.
var ErrNotGitRepo = errors.New("not the root of a git repository")

// Plan is the deterministic layout of one chat's worktree. The backend derives
// Path from it to lock the directory before the runner creates it.
type Plan struct {
	// RepoDir is the main checkout the worktree is made from.
	RepoDir string
	// Path is the worktree directory, a child of the configured root.
	Path string
	// Name is the worktree's name (the basename of Path).
	Name string
	// RefNamespace is the chat's branch namespace below refs/heads.
	RefNamespace string
	// Branch is the branch checked out in the worktree.
	Branch string
	// ObjectsDir is the chat's private object directory.
	ObjectsDir string
}

// PlanFor lays out the worktree of chatID for repoDir under root. objectsBase
// is the directory holding the chat's private object store. All inputs must be
// absolute; PlanFor touches nothing.
func PlanFor(root, repoDir, chatID, objectsBase string) (Plan, error) {
	if !chatIDRe.MatchString(chatID) {
		return Plan{}, fmt.Errorf("invalid chat id %q for a worktree", chatID)
	}
	for _, p := range []struct{ what, path string }{{"root", root}, {"repository", repoDir}, {"objects", objectsBase}} {
		if !filepath.IsAbs(p.path) {
			return Plan{}, fmt.Errorf("worktree %s must be an absolute path: %q", p.what, p.path)
		}
	}
	repoDir, root = filepath.Clean(repoDir), filepath.Clean(root)
	if repoDir == "/" {
		return Plan{}, errors.New("cannot create a worktree of /")
	}
	if pathsOverlap(root, repoDir) {
		return Plan{}, fmt.Errorf("worktree root %q overlaps repository %q", root, repoDir)
	}
	name := unsafeRe.ReplaceAllString(filepath.Base(repoDir), "_") + "-" + chatID
	ns := BranchPrefix + "/" + chatID
	return Plan{
		RepoDir:      repoDir,
		Path:         filepath.Join(root, name),
		Name:         name,
		RefNamespace: ns,
		Branch:       ns + "/" + BranchName,
		ObjectsDir:   filepath.Join(filepath.Clean(objectsBase), "git-objects"),
	}, nil
}

// PathFor returns where the worktree of chatID for repoDir lives under root,
// without touching anything. The backend uses it to lock the directory the
// runner is about to create.
func PathFor(root, repoDir, chatID string) (string, error) {
	p, err := PlanFor(root, repoDir, chatID, "/")
	if err != nil {
		return "", err
	}
	return p.Path, nil
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// GitDir is the main repository's git directory.
func (p Plan) GitDir() string { return filepath.Join(p.RepoDir, ".git") }

// Run is the sandbox-facing description of the worktree.
func (p Plan) Run() *agentspec.WorktreeRun {
	return &agentspec.WorktreeRun{
		GitDir:       p.GitDir(),
		Name:         p.Name,
		RefNamespace: p.RefNamespace,
		ObjectsDir:   p.ObjectsDir,
	}
}

// Ensure makes the worktree exist and returns its sandbox description. It is
// idempotent: an existing worktree of this plan is reused, which is how a
// resumed chat finds its work again.
func (p Plan) Ensure(ctx context.Context) (*agentspec.WorktreeRun, error) {
	if err := p.vetRepo(ctx); err != nil {
		return nil, err
	}
	switch linked, err := p.linked(); {
	case err != nil:
		return nil, err
	case !linked:
		if err := p.add(ctx); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{
		p.ObjectsDir,
		filepath.Join(p.GitDir(), "refs", "heads", filepath.FromSlash(p.RefNamespace)),
		filepath.Join(p.GitDir(), "logs", "refs", "heads", filepath.FromSlash(p.RefNamespace)),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating %q: %w", dir, err)
		}
	}
	return p.Run(), nil
}

// linked reports whether Path is already this plan's worktree. A directory
// that exists but is anything else is an error, never silently reused.
func (p Plan) linked() (bool, error) {
	fi, err := os.Lstat(p.Path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("worktree path %q exists and is not a directory", p.Path)
	}
	gf := filepath.Join(p.Path, ".git")
	if gfi, err := os.Lstat(gf); err != nil || !gfi.Mode().IsRegular() {
		return false, fmt.Errorf("worktree path %q exists but is not a git worktree", p.Path)
	}
	data, err := os.ReadFile(gf)
	if err != nil {
		return false, err
	}
	want := "gitdir: " + filepath.Join(p.GitDir(), "worktrees", p.Name)
	if strings.TrimSpace(string(data)) != want {
		return false, fmt.Errorf("worktree path %q belongs to another repository", p.Path)
	}
	if err := p.vetAdminDir(); err != nil {
		return false, err
	}
	return true, nil
}

// vetAdminDir checks the worktree's administrative directory, which the agent
// can write: git must still resolve it to the main repository, otherwise a
// rewritten commondir would point the runner's git at a repository (and its
// config) the agent controls.
func (p Plan) vetAdminDir() error {
	admin := filepath.Join(p.GitDir(), "worktrees", p.Name)
	bad := func(why string) error {
		return fmt.Errorf("worktree %q is tampered with: %s", p.Name, why)
	}
	for file, want := range map[string]string{
		"commondir": "../..",
		"gitdir":    filepath.Join(p.Path, ".git"),
	} {
		data, err := os.ReadFile(filepath.Join(admin, file))
		if err != nil || strings.TrimSpace(string(data)) != want {
			return bad(file + " does not point to the main repository")
		}
	}
	if _, err := os.Lstat(filepath.Join(admin, "config.worktree")); err == nil {
		return bad("it carries its own config")
	}
	return filepath.WalkDir(admin, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return bad(err.Error())
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return bad("symlink " + d.Name())
		}
		return nil
	})
}

func (p Plan) add(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return fmt.Errorf("creating worktree root: %w", err)
	}
	// An existing branch (the worktree was removed but the work kept) is
	// checked out again; otherwise the branch starts at the current HEAD.
	args := []string{"worktree", "add", "-b", p.Branch, p.Path}
	if _, err := p.git(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.Branch); err == nil {
		args = []string{"worktree", "add", p.Path, p.Branch}
	}
	if _, err := p.git(ctx, args...); err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	if linked, err := p.linked(); err != nil || !linked {
		return fmt.Errorf("git created the worktree under an unexpected name (is %q already registered?)", p.Name)
	}
	return nil
}

// Remove deletes the worktree, its private objects and, once merged, its
// branch. Without force a worktree with uncommitted changes, or a branch that
// is not merged into HEAD, is kept and an error returned.
func (p Plan) Remove(ctx context.Context, force bool) error {
	if err := p.vetRepo(ctx); err != nil {
		return err
	}
	if linked, err := p.linked(); err != nil {
		return err
	} else if linked {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := p.git(ctx, append(args, p.Path)...); err != nil {
			return fmt.Errorf("removing worktree: %w", err)
		}
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := p.git(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.Branch); err == nil {
		if _, err := p.git(ctx, "branch", flag, p.Branch); err != nil {
			return fmt.Errorf("deleting branch %s: %w", p.Branch, err)
		}
	}
	if err := os.RemoveAll(p.ObjectsDir); err != nil {
		return fmt.Errorf("removing private objects: %w", err)
	}
	return nil
}

// vetRepo refuses anything but the plain root of a checkout whose local config
// cannot make git run a program.
func (p Plan) vetRepo(ctx context.Context) error {
	fi, err := os.Lstat(p.GitDir())
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotGitRepo, p.RepoDir)
	}
	for _, name := range []string{"commondir", "gitdir"} {
		if _, err := os.Lstat(filepath.Join(p.GitDir(), name)); err == nil {
			return fmt.Errorf("%w: %s redirects git elsewhere", ErrNotGitRepo, p.GitDir())
		}
	}
	out, err := p.git(ctx, "config", "--local", "--no-includes", "--list", "-z")
	if err != nil {
		return fmt.Errorf("repository config could not be vetted: %w", err)
	}
	for _, kv := range strings.Split(out, "\x00") {
		key, _, _ := strings.Cut(kv, "\n")
		if k := strings.ToLower(key); runnerfs.DangerousGitConfigKey(k) {
			return fmt.Errorf("repository config %q is not allowed on the runner", k)
		}
	}
	return nil
}

// git runs a hardened git command in the main checkout.
func (p Plan) git(ctx context.Context, args ...string) (string, error) {
	return gitIn(ctx, p.RepoDir, nil, args...)
}

func gitIn(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append(runnerfs.HardenedGitArgs(), args...)...)
	cmd.Dir = dir
	cmd.Env = append(runnerfs.HardenedGitEnv(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=0")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}
