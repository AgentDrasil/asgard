package bwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// appendWorktreeArgs makes a chat's git worktree usable inside a sandbox whose
// run directory is that worktree. The worktree's .git file points into the
// main repository's git directory, which is bound read-only, with only the
// per-chat paths writable on top:
//
//   - worktrees/<name>: HEAD, index and rebase state of this worktree
//   - refs/heads/<ns> and logs/refs/heads/<ns>: this chat's branches and reflogs
//   - the chat's private object directory; the shared objects are an alternate
//
// Every other ref, the repository config, the shared objects and the main
// working tree stay read-only. The binds must come after all read-write binds so the read-only
// git directory is not shadowed by an ancestor mount.
func appendWorktreeArgs(args []string, cfg *agentspec.AgentConfig, readonlyDirs []string) ([]string, error) {
	if cfg == nil || cfg.WorktreeRun == nil {
		return args, nil
	}
	wr := cfg.WorktreeRun
	if err := validateWorktreeRun(wr); err != nil {
		return nil, err
	}
	if beneathAny(readonlyDirs, wr.GitDir) {
		return nil, fmt.Errorf("git directory %q lies under a readonly mount", wr.GitDir)
	}

	rwDirs := []string{
		filepath.Join(wr.GitDir, "worktrees", wr.Name),
		filepath.Join(wr.GitDir, "refs", "heads", filepath.FromSlash(wr.RefNamespace)),
		filepath.Join(wr.GitDir, "logs", "refs", "heads", filepath.FromSlash(wr.RefNamespace)),
		wr.ObjectsDir,
	}
	// The whole main checkout is read-only (the agent can read it to compare),
	// also where an ancestor such as $HOME is bound writable.
	if mainDir := filepath.Dir(wr.GitDir); mainDir != "/" {
		args = append(args, "--ro-bind", mainDir, mainDir)
	}
	args = append(args, "--ro-bind", wr.GitDir, wr.GitDir)
	for _, dir := range rwDirs {
		if err := requireDir(dir); err != nil {
			return nil, fmt.Errorf("worktree path: %w", err)
		}
		args = append(args, "--bind", dir, dir)
	}

	// The shared object store is only readable; new objects go to the private
	// directory. Automatic gc/maintenance would fail on the read-only repo.
	for _, kv := range [][2]string{
		{"GIT_OBJECT_DIRECTORY", wr.ObjectsDir},
		{"GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(wr.GitDir, "objects")},
		{"GIT_CONFIG_COUNT", "2"},
		{"GIT_CONFIG_KEY_0", "gc.auto"},
		{"GIT_CONFIG_VALUE_0", "0"},
		{"GIT_CONFIG_KEY_1", "maintenance.auto"},
		{"GIT_CONFIG_VALUE_1", "false"},
	} {
		args = append(args, "--setenv", kv[0], kv[1])
	}
	return args, nil
}

// worktreePrompt is appended to the contract of an agent running in a chat
// worktree. It stays short: what to do after a failed merge is told by
// asgard-merge itself, case by case.
func worktreePrompt(wr *agentspec.WorktreeRun) string {
	if wr.Shared {
		return fmt.Sprintf("IMPORTANT: You work in a git worktree shared with the agent that spawned you, not in the main checkout. Your working directory is the worktree; you may only create and move branches under `%[1]s/`. "+
			"The main checkout (see `git worktree list`) is not yours: never edit it, and do not run `git checkout` on its branch, `git push`, `git gc` or change git config. "+
			"Git may print `Unable to create packed-refs.lock: Read-only file system`; it is harmless, ignore it. "+
			"Do the task you were given and leave the changes in the worktree (commit them only if asked to). Do not merge or deliver the work: the agent that spawned you does that.", wr.RefNamespace)
	}
	return fmt.Sprintf("IMPORTANT: You work in your own git worktree, not in the main checkout. Your working directory is the worktree and `git branch --show-current` is your branch; you may only create and move branches under `%[1]s/`. "+
		"The main checkout (see `git worktree list`) is not yours: never edit it, and do not run `git checkout` on its branch, `git push`, `git gc` or change git config. "+
		"Git may print `Unable to create packed-refs.lock: Read-only file system`; it is harmless, ignore it.\n\n"+
		"To deliver your work: (1) commit everything; (2) `git rebase` onto the branch of the main checkout and resolve any conflicts; (3) run the project's checks (lint, build, tests as the project instructions say) and fix what fails; (4) run `asgard-merge`. "+
		"If asgard-merge fails it prints exactly what to do next (for example the main branch moved: rebase again, re-run the checks, then run asgard-merge again); follow it. "+
		"Do not report the work as done until asgard-merge says it merged. If it keeps failing after five attempts, stop and tell the user what blocks it.", wr.RefNamespace)
}

func validateWorktreeRun(wr *agentspec.WorktreeRun) error {
	for _, p := range []struct{ name, path string }{{"git dir", wr.GitDir}, {"objects dir", wr.ObjectsDir}} {
		if !filepath.IsAbs(p.path) {
			return fmt.Errorf("worktree %s must be an absolute path: %q", p.name, p.path)
		}
	}
	if wr.Name == "" || strings.ContainsAny(wr.Name, `/\`) || wr.Name == "." || wr.Name == ".." {
		return fmt.Errorf("invalid worktree name %q", wr.Name)
	}
	ns := wr.RefNamespace
	if ns == "" || strings.HasPrefix(ns, "/") || strings.HasSuffix(ns, "/") {
		return fmt.Errorf("invalid worktree ref namespace %q", ns)
	}
	for _, seg := range strings.Split(ns, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, ".lock") {
			return fmt.Errorf("invalid worktree ref namespace %q", ns)
		}
	}
	return nil
}

func requireDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	return nil
}
