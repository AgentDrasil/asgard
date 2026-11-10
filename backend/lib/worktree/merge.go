package worktree

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

// MergeStatus is the outcome of a merge attempt. The agent gets it back
// together with Guidance and resolves the failure itself.
type MergeStatus string

const (
	MergeMerged         MergeStatus = "merged"
	MergeNothing        MergeStatus = "nothing_to_merge"
	MergeMainMoved      MergeStatus = "main_moved"
	MergeDirtyWorktree  MergeStatus = "dirty_worktree"
	MergeMainDirty      MergeStatus = "main_dirty"
	MergeMainDetached   MergeStatus = "main_detached"
	MergeInvalidObjects MergeStatus = "invalid_objects"
)

// MergeResult reports a merge attempt.
type MergeResult struct {
	Status MergeStatus
	// Target is the branch of the main checkout the work was merged into.
	Target string
	// Commit is the commit that is now the tip of Target (MergeMerged), or the
	// tip of the worktree branch otherwise.
	Commit string
	// Detail carries the specifics (file names, git output).
	Detail string
}

// Guidance tells the agent what to do next for the result. Failures are
// handled by the agent, which is why every status ends with a concrete step.
func (r MergeResult) Guidance() string {
	switch r.Status {
	case MergeMerged:
		return fmt.Sprintf("Merged into %s (now at %s). The worktree stays; for more work, continue on your branch and merge again.", r.Target, short(r.Commit))
	case MergeNothing:
		return fmt.Sprintf("Nothing to merge: your branch has no commits that %s lacks.", r.Target)
	case MergeMainMoved:
		return fmt.Sprintf("%s has new commits that your branch does not contain. Run `git rebase %s` in your worktree, resolve any conflicts, re-run the project's checks (lint, build, test), then run asgard-merge again.", r.Target, r.Target)
	case MergeDirtyWorktree:
		return "Your worktree has uncommitted changes (below). Commit them (or discard what you do not want to keep), then run asgard-merge again.\n" + r.Detail
	case MergeMainDirty:
		return fmt.Sprintf("The main checkout of %s has uncommitted changes, so it cannot be fast-forwarded. This is not yours to fix: report it to the user and stop.\n%s", r.Target, r.Detail)
	case MergeMainDetached:
		return "The main checkout is not on a branch, so there is nothing to merge into. Report it to the user and stop."
	case MergeInvalidObjects:
		return "Git could not read or verify your branch:\n" + r.Detail + "\nRecreate the offending commits (for example `git rebase` or amend) and run asgard-merge again."
	}
	return string(r.Status)
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// Merge fast-forwards the main checkout's current branch to the worktree
// branch. The caller must hold the exclusive lock on the main checkout. The
// worktree is deliberately not touched afterwards: the chat usually goes on.
//
// The agent's commits live in its private object directory, so before the
// branch moves the objects it adds are packed out of it and imported with a
// strict check; the shared object store is never written by the agent.
func (p Plan) Merge(ctx context.Context) (MergeResult, error) {
	if err := p.vetRepo(ctx); err != nil {
		return MergeResult{}, err
	}
	linked, err := p.linked()
	if err != nil {
		return MergeResult{}, err
	}
	if !linked {
		return MergeResult{}, fmt.Errorf("worktree %q does not exist", p.Path)
	}

	// Uncommitted work would be left behind (and lost on cleanup).
	if out, err := gitIn(ctx, p.Path, p.objectsEnv(), "status", "--porcelain"); err != nil {
		// Git cannot even read the checked-out state: broken objects.
		return MergeResult{Status: MergeInvalidObjects, Detail: err.Error()}, nil
	} else if out = strings.TrimSpace(out); out != "" {
		return MergeResult{Status: MergeDirtyWorktree, Detail: capLines(out, 20)}, nil
	}

	target, err := p.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return MergeResult{Status: MergeMainDetached}, nil
	}
	target = strings.TrimSpace(target)
	if out, err := p.git(ctx, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return MergeResult{}, fmt.Errorf("checking main checkout status: %w", err)
	} else if out = strings.TrimSpace(out); out != "" {
		return MergeResult{Status: MergeMainDirty, Target: target, Detail: capLines(out, 20)}, nil
	}

	tip, err := p.git(ctx, "rev-parse", "--verify", "refs/heads/"+p.Branch)
	if err != nil {
		return MergeResult{}, fmt.Errorf("worktree branch %s: %w", p.Branch, err)
	}
	tip = strings.TrimSpace(tip)
	res := MergeResult{Target: target, Commit: tip}

	// Everything below reads the agent's objects through the alternate.
	env := p.objectsEnv()
	if head, _ := p.git(ctx, "rev-parse", "--verify", "refs/heads/"+target); strings.TrimSpace(head) == tip {
		res.Status = MergeNothing
		return res, nil
	}
	if _, err := gitIn(ctx, p.RepoDir, env, "merge-base", "--is-ancestor", "refs/heads/"+target, tip); err != nil {
		// Not a descendant: either the branch is unreadable or main moved on.
		if _, gerr := gitIn(ctx, p.RepoDir, env, "cat-file", "-e", tip+"^{commit}"); gerr != nil {
			res.Status, res.Detail = MergeInvalidObjects, err.Error()
			return res, nil
		}
		res.Status = MergeMainMoved
		return res, nil
	}

	if err := p.importObjects(ctx, tip, target); err != nil {
		res.Status, res.Detail = MergeInvalidObjects, err.Error()
		return res, nil
	}
	if _, err := p.git(ctx, "merge", "--ff-only", tip); err != nil {
		return MergeResult{}, fmt.Errorf("fast-forwarding %s: %w", target, err)
	}
	res.Status = MergeMerged
	return res, nil
}

func (p Plan) objectsEnv() []string {
	return []string{"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + p.ObjectsDir}
}

// importObjects copies the objects reachable from tip but not from target out
// of the private object directory into the shared store, verifying them.
func (p Plan) importObjects(ctx context.Context, tip, target string) error {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	base := runnerfs.HardenedGitArgs()
	env := append(runnerfs.HardenedGitEnv(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=0")

	pack := exec.CommandContext(ctx, "git", append(append([]string{}, base...), "pack-objects", "--revs", "--stdout", "--quiet")...)
	pack.Dir, pack.Env = p.RepoDir, append(append([]string{}, env...), p.objectsEnv()...)
	pack.Stdin = strings.NewReader(tip + "\n^refs/heads/" + target + "\n")

	index := exec.CommandContext(ctx, "git", append(append([]string{}, base...), "index-pack", "--stdin", "--strict")...)
	index.Dir, index.Env = p.RepoDir, env

	var packErr, indexErr, indexOut bytes.Buffer
	pack.Stderr, index.Stderr, index.Stdout = &packErr, &indexErr, &indexOut
	pr, pw := io.Pipe()
	pack.Stdout, index.Stdin = pw, pr

	if err := index.Start(); err != nil {
		return err
	}
	if err := pack.Start(); err != nil {
		_ = pw.Close()
		_ = index.Wait()
		return err
	}
	packWait := pack.Wait()
	_ = pw.CloseWithError(packWait)
	indexWait := index.Wait()
	if packWait != nil {
		return fmt.Errorf("packing objects: %v: %s", packWait, strings.TrimSpace(packErr.String()))
	}
	if indexWait != nil {
		return fmt.Errorf("verifying objects: %v: %s", indexWait, strings.TrimSpace(indexErr.String()))
	}
	return nil
}

func capLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… and %d more", len(lines)-n)
}
