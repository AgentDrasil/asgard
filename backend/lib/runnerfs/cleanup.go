package runnerfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/AgentDrasil/asgard/backend/lib/agents"

	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// CleanupRoots are the runner-local roots that hold per-chat directories:
// <Tmp>/<chat>, <Sessions>/<chat> and <CA>/<chat> (the merged CA bundle).
// CA normally lies inside Tmp (runnerpaths.CABundleDirName).
type CleanupRoots struct {
	Tmp, Sessions, CA string
}

// DefaultCleanupRoots are the runner's own data dirs.
func DefaultCleanupRoots() CleanupRoots {
	return CleanupRoots{Tmp: runnerpaths.TmpDir(), Sessions: runnerpaths.SessionsDir(), CA: runnerpaths.CABundleBaseDir()}
}

// CleanupTmpSweep removes dangling atomic-write temp files (`.<base>.*.tmp`)
// below Dir ("tmp" or "session") / ChatID / Rel (mirrors
// dbmodels.CleanOrphanTmpFiles).
type CleanupTmpSweep struct {
	ChatID string
	// Scope is "tmp" or "session".
	Scope string
	Rel   string
}

// CleanupSpec is one cleanup request. A zero Cutoff disables the orphan sweep.
type CleanupSpec struct {
	ChatIDs   []string
	Cutoff    time.Time
	Keep      []string
	TmpSweeps []CleanupTmpSweep
}

// CleanupFailure is a per-chat failure ("" ChatID: the orphan sweep).
type CleanupFailure struct {
	ChatID string
	Err    string
}

// CleanupResult is the outcome of a cleanup.
type CleanupResult struct {
	// Removed counts the directories removed.
	Removed  int
	Failures []CleanupFailure
}

// Cleanup applies spec beneath the roots. Every path is derived from a
// validated chat id or from a directory entry name read beneath a root fd, and
// removal never follows symlinks, so nothing outside the roots is touched.
func (r CleanupRoots) Cleanup(spec CleanupSpec) CleanupResult {
	var res CleanupResult
	fail := func(chat string, err error) {
		res.Failures = append(res.Failures, CleanupFailure{ChatID: chat, Err: err.Error()})
	}

	seen := make(map[string]bool, len(spec.ChatIDs))
	for _, id := range spec.ChatIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if !agents.IsValidChatID(id) {
			fail(id, Errf(CodeBadRequest, "invalid chat id"))
			continue
		}
		var errs []error
		for _, root := range []string{r.Tmp, r.Sessions, r.CA} {
			n, err := removeChild(root, id)
			res.Removed += n
			if err != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			fail(id, err)
		}
	}

	for _, s := range spec.TmpSweeps {
		if err := r.sweepTmp(s); err != nil {
			fail(s.ChatID, err)
		}
	}

	if !spec.Cutoff.IsZero() {
		keep := make(map[string]bool, len(spec.Keep))
		for _, id := range spec.Keep {
			keep[id] = true
		}
		skipTmp := ""
		if filepath.Dir(filepath.Clean(r.CA)) == filepath.Clean(r.Tmp) {
			skipTmp = filepath.Base(r.CA)
		}
		for _, sw := range []struct{ root, skip string }{{r.Tmp, skipTmp}, {r.Sessions, ""}, {r.CA, ""}} {
			n, errs := sweepOrphans(sw.root, sw.skip, keep, spec.Cutoff)
			res.Removed += n
			for _, err := range errs {
				fail("", err)
			}
		}
	}
	return res
}

// openOptionalRoot opens a root; a missing root is not an error (nil, nil).
func openOptionalRoot(root string) (int, bool, error) {
	if root == "" {
		return -1, false, nil
	}
	fd, err := openRootStrict(root)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return -1, false, nil
		}
		return -1, false, err
	}
	return fd, true, nil
}

// removeChild removes root/name (without following symlinks) and returns 1
// when a directory was removed (a removed symlink or file counts 0).
func removeChild(root, name string) (int, error) {
	fd, ok, err := openOptionalRoot(root)
	if err != nil || !ok {
		return 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return 0, nil
		}
		return 0, mapErrno(err)
	}
	if err := removeAllAt(fd, name, nil); err != nil {
		return 0, fmt.Errorf("remove %s: %w", filepath.Join(root, name), err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return 0, nil
	}
	return 1, nil
}

// sweepOrphans removes the directories directly below root that are not in
// keep and whose latest mtime is older than cutoff.
func sweepOrphans(root, skip string, keep map[string]bool, cutoff time.Time) (int, []error) {
	fd, ok, err := openOptionalRoot(root)
	if err != nil {
		return 0, []error{err}
	}
	if !ok {
		return 0, nil
	}
	defer func() { _ = unix.Close(fd) }()
	d, err := dupDir(fd)
	if err != nil {
		return 0, []error{err}
	}
	names, rerr := d.Readdirnames(-1)
	_ = d.Close()
	if rerr != nil {
		return 0, []error{mapErrno(rerr)}
	}
	removed := 0
	var errs []error
	for _, n := range names {
		if n == skip || keep[n] {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		if !latestModTime(fd, n, statTime(&st)).Before(cutoff) {
			continue
		}
		if err := removeAllAt(fd, n, nil); err != nil {
			errs = append(errs, fmt.Errorf("remove orphan dir %s: %w", filepath.Join(root, n), err))
			continue
		}
		removed++
	}
	return removed, errs
}

func statTime(st *unix.Stat_t) time.Time { return time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)) }

func dupDir(fd int) (*os.File, error) {
	nfd, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno(err)
	}
	return os.NewFile(uintptr(nfd), "dir"), nil
}

// latestModTime is the newest mtime of name (a directory below dirfd) and
// everything in it; symlinks are never followed.
func latestModTime(dirfd int, name string, latest time.Time) time.Time {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return latest
	}
	d := os.NewFile(uintptr(fd), name)
	defer func() { _ = d.Close() }()
	names, _ := d.Readdirnames(-1)
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		if t := statTime(&st); t.After(latest) {
			latest = t
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			latest = latestModTime(fd, n, latest)
		}
	}
	return latest
}

// sweepTmp removes `.*.tmp` non-directories below the sweep dir. The path is
// resolved beneath the root without following any symlink; a missing dir is
// fine.
func (r CleanupRoots) sweepTmp(s CleanupTmpSweep) error {
	if !agents.IsValidChatID(s.ChatID) {
		return Errf(CodeBadRequest, "invalid chat id")
	}
	var root string
	switch s.Scope {
	case "tmp":
		root = r.Tmp
	case "session":
		root = r.Sessions
	default:
		return Errf(CodeBadRequest, "orphan tmp sweep scope must be tmp or session")
	}
	if s.Rel != "" && !filepath.IsLocal(s.Rel) {
		return Errf(CodeDenied, "access denied: sweep dir escapes the %s dir", s.Scope)
	}
	rootfd, ok, err := openOptionalRoot(root)
	if err != nil || !ok {
		return err
	}
	defer func() { _ = unix.Close(rootfd) }()
	fd, err := openResolve(rootfd, filepath.Join(s.ChatID, s.Rel), unix.O_RDONLY|unix.O_DIRECTORY, 0, strictFlags)
	if err != nil {
		if c := CodeOf(err); c == CodeNotFound || c == CodeBadRequest {
			return nil
		}
		return err
	}
	return sweepTmpFd(fd)
}

// sweepTmpFd takes ownership of fd.
func sweepTmpFd(fd int) error {
	d := os.NewFile(uintptr(fd), "sweep")
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return mapErrno(err)
	}
	var errs []error
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			sub, err := unix.Openat(fd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			if err := sweepTmpFd(sub); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".tmp") {
			_ = unix.Unlinkat(fd, n, 0)
		}
	}
	return errors.Join(errs...)
}
