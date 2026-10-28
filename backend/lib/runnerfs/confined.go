package runnerfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// All file access is anchored at a root directory fd and resolved beneath it:
// openat2 with RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS refuses any symlink or
// ".." that leaves the root, including a dangling final symlink that O_CREAT
// would otherwise follow. Kernels without openat2 (ENOSYS) fall back to
// walking the components with O_NOFOLLOW, which fails closed on every symlink.

var noOpenat2 atomic.Bool

const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS

const strictFlags = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

func mapErrno(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOENT):
		return Errf(CodeNotFound, "not found")
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.ELOOP):
		return Errf(CodeDenied, "access denied: path escapes the chat's directories")
	case errors.Is(err, unix.EISDIR):
		return Errf(CodeIsDir, "requested path is a directory, not a file")
	case errors.Is(err, unix.ENOTDIR):
		return Errf(CodeBadRequest, "a path component is not a directory")
	}
	return Errf(CodeInternal, "%v", err)
}

// openBeneath opens rel (relative, no leading slash) under dirfd.
func openBeneath(dirfd int, rel string, flags int, mode uint32) (int, error) {
	return openResolve(dirfd, rel, flags, mode, resolveFlags)
}

// openResolve is openBeneath with explicit openat2 resolve flags.
func openResolve(dirfd int, rel string, flags int, mode uint32, resolve uint64) (int, error) {
	rel = filepath.Clean(rel)
	if rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return -1, Errf(CodeDenied, "access denied: path escapes the chat's directories")
	}
	flags |= unix.O_CLOEXEC
	if !noOpenat2.Load() {
		for {
			fd, err := unix.Openat2(dirfd, rel, &unix.OpenHow{Flags: uint64(flags), Mode: uint64(mode), Resolve: resolve})
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.ENOSYS) {
				noOpenat2.Store(true)
				break
			}
			return fd, mapErrno(err)
		}
	}
	return openWalk(dirfd, rel, flags, mode)
}

// openWalk is the openat2-less fallback: every parent component is opened with
// O_NOFOLLOW|O_DIRECTORY from the previous fd and the final one with O_NOFOLLOW.
func openWalk(dirfd int, rel string, flags int, mode uint32) (int, error) {
	parts := strings.Split(rel, string(filepath.Separator))
	cur, err := unix.Dup(dirfd)
	if err != nil {
		return -1, mapErrno(err)
	}
	for i, p := range parts {
		f, m := unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, uint32(0)
		if i == len(parts)-1 {
			f, m = flags|unix.O_NOFOLLOW, mode
		}
		next, err := unix.Openat(cur, p, f, m)
		_ = unix.Close(cur)
		if err != nil {
			return -1, mapErrno(err)
		}
		cur = next
	}
	return cur, nil
}

// mkdirAllBeneath opens (creating missing components) the directory rel under
// dirfd; rel "." returns a dup of dirfd.
func mkdirAllBeneath(dirfd int, rel string, create bool, g *guard) (int, error) {
	rel = filepath.Clean(rel)
	if rel == "." {
		fd, err := unix.Dup(dirfd)
		return fd, mapErrno(err)
	}
	if !create {
		return openBeneath(dirfd, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	}
	cur, err := unix.Dup(dirfd)
	if err != nil {
		return -1, mapErrno(err)
	}
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		next, err := openBeneath(cur, p, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if CodeOf(err) == CodeNotFound {
			if gerr := g.denyWrite(cur, p); gerr != nil {
				_ = unix.Close(cur)
				return -1, gerr
			}
			if merr := unix.Mkdirat(cur, p, 0o755); merr != nil && !errors.Is(merr, unix.EEXIST) {
				_ = unix.Close(cur)
				return -1, mapErrno(merr)
			}
			next, err = openBeneath(cur, p, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		}
		_ = unix.Close(cur)
		if err != nil {
			return -1, err
		}
		cur = next
	}
	return cur, nil
}

// openRoot opens the root directory itself (the root path is trusted: it is
// derived from the runner's layout or a validated allowed dir).
func openRoot(root string) (int, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	return fd, mapErrno(err)
}

// openRegular opens rel for reading and refuses anything but a regular file
// (O_NONBLOCK keeps a FIFO from blocking the open).
func openRegular(rootfd int, rel string) (*os.File, os.FileInfo, error) {
	fd, err := openBeneath(rootfd, rel, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Base(rel))
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, Errf(CodeInternal, "failed to stat file: %v", err)
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, nil, Errf(CodeIsDir, "requested path is a directory, not a file")
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, Errf(CodeBadRequest, "not a regular file")
	}
	_ = unix.SetNonblock(fd, false)
	return f, info, nil
}

// parentOf opens the parent directory of rel under rootfd and returns it with
// the final component.
func parentOf(rootfd int, rel string, mkdir bool, g *guard) (int, string, error) {
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return -1, "", Errf(CodeDenied, "access denied: path escapes the chat's directories")
	}
	pfd, err := mkdirAllBeneath(rootfd, filepath.Dir(rel), mkdir, g)
	return pfd, filepath.Base(rel), err
}

func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// capReader fails with CodeTooLarge once more than max bytes were read.
type capReader struct {
	r    io.Reader
	left int64
	msg  string
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, Errf(CodeTooLarge, "%s", c.msg)
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, Errf(CodeTooLarge, "%s", c.msg)
	}
	return n, err
}

// writeBeneath writes r to rel under rootfd (see Local.WriteFile).
func writeBeneath(rootfd int, rel string, req WriteRequest, mode os.FileMode, r io.Reader, g *guard) (int64, error) {
	pfd, base, err := parentOf(rootfd, rel, req.Mkdir, g)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(pfd) }()
	if err := g.denyWrite(pfd, base); err != nil {
		return 0, err
	}
	r = &capReader{r: r, left: MaxWriteSize, msg: "write exceeds the maximum size"}

	name, flags := base, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC
	if req.Atomic {
		name, flags = "."+base+"."+randSuffix()+".tmp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC
	}
	fd, err := unix.Openat(pfd, name, flags, uint32(mode.Perm()))
	if err != nil {
		return 0, mapErrno(err)
	}
	f := os.NewFile(uintptr(fd), name)
	n, err := io.Copy(f, r)
	if err == nil && req.Atomic {
		err = f.Chmod(mode)
	}
	if err == nil && req.Sync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && req.Atomic {
		if err = unix.Renameat(pfd, name, pfd, base); err == nil && req.Sync {
			_ = unix.Fsync(pfd)
		}
	}
	if err != nil {
		if req.Atomic || CodeOf(err) == CodeTooLarge {
			_ = unix.Unlinkat(pfd, name, 0)
		}
		if CodeOf(err) == CodeTooLarge {
			return n, err
		}
		return n, Errf(CodeInternal, "failed to write file: %v", err)
	}
	return n, nil
}

func appendBeneath(rootfd int, rel string, mkdir bool, data []byte, sync bool, g *guard) error {
	pfd, base, err := parentOf(rootfd, rel, mkdir, g)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pfd) }()
	if err := g.denyWrite(pfd, base); err != nil {
		return err
	}
	fd, err := unix.Openat(pfd, base, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return mapErrno(err)
	}
	f := os.NewFile(uintptr(fd), base)
	_, err = f.Write(data)
	if err == nil && sync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Errf(CodeInternal, "failed to append: %v", err)
	}
	return nil
}

func removeBeneath(rootfd int, rel string, recursive bool, g *guard) error {
	pfd, base, err := parentOf(rootfd, rel, false, g)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil
		}
		return err
	}
	defer func() { _ = unix.Close(pfd) }()
	if err := g.denyWrite(pfd, base); err != nil {
		return err
	}
	if recursive {
		// A readonly mount below the target must survive: refuse up front.
		if err := g.denyTree(pfd, base); err != nil {
			return err
		}
		return removeAllAt(pfd, base, g)
	}
	err = unix.Unlinkat(pfd, base, 0)
	if errors.Is(err, unix.EISDIR) || errors.Is(err, unix.EPERM) {
		err = unix.Unlinkat(pfd, base, unix.AT_REMOVEDIR)
	}
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return mapErrno(err)
	}
	return nil
}

// removeAllAt removes name and everything below it without following symlinks.
func removeAllAt(pfd int, name string, g *guard) error {
	err := unix.Unlinkat(pfd, name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if !errors.Is(err, unix.EISDIR) && !errors.Is(err, unix.EPERM) {
		return mapErrno(err)
	}
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapErrno(err)
	}
	d := os.NewFile(uintptr(fd), name)
	// Check the directory by its real path (a symlinked alias cannot hide a
	// readonly mount) before touching its entries.
	if err := g.denyDirFd(fd); err != nil {
		_ = d.Close()
		return err
	}
	names, err := d.Readdirnames(-1)
	if err != nil {
		_ = d.Close()
		return mapErrno(err)
	}
	for _, n := range names {
		if err := removeAllAt(fd, n, g); err != nil {
			_ = d.Close()
			return err
		}
	}
	_ = d.Close()
	if err := unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return mapErrno(err)
	}
	return nil
}

func statBeneath(rootfd int, rel string) (os.FileInfo, error) {
	fd, err := openBeneath(rootfd, rel, unix.O_PATH, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, Errf(CodeInternal, "failed to stat: %v", err)
	}
	return info, nil
}

// openRootStrict opens an absolute root directory without following a symlink
// in any component (openat2 from "/" with RESOLVE_NO_SYMLINKS; the ENOSYS
// fallback walks with O_NOFOLLOW). A root that is, or lies under, a symlink
// is refused: fail closed rather than let a swapped link move the anchor.
func openRootStrict(root string) (int, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return -1, Errf(CodeBadRequest, "root %q is not absolute", root)
	}
	slash, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, mapErrno(err)
	}
	defer func() { _ = unix.Close(slash) }()
	if root == "/" {
		return unix.Dup(slash)
	}
	rel := strings.TrimPrefix(root, "/")
	var fd int
	if noOpenat2.Load() {
		fd, err = openWalk(slash, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	} else {
		fd, err = openResolve(slash, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0, strictFlags)
	}
	if err != nil {
		if CodeOf(err) == CodeDenied {
			return -1, Errf(CodeDenied, "access denied: root %q is or is under a symlink", root)
		}
		return -1, err
	}
	return fd, nil
}

// guard vetoes mutations of readonly mounts (bwrap's --ro-bind over a --bind).
// All checks use the real path of an already open directory fd, so the check
// and the mutation that follows act on the very same directory. A nil guard
// allows everything.
type guard struct{ ro []string }

func newGuard(ro []string) *guard {
	g := &guard{}
	for _, d := range ro {
		if !filepath.IsAbs(d) {
			continue
		}
		g.ro = append(g.ro, filepath.Clean(d))
		if ev, err := filepath.EvalSymlinks(d); err == nil {
			g.ro = append(g.ro, ev)
		}
	}
	if len(g.ro) == 0 {
		return nil
	}
	return g
}

func fdPath(fd int) (string, error) {
	p, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return "", Errf(CodeInternal, "cannot resolve directory: %v", err)
	}
	return p, nil
}

var errReadonly = Errf(CodeDenied, "access denied: path is beneath a readonly mount")

func beneath(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// denyWrite refuses name inside dirfd when it lies in a readonly mount.
func (g *guard) denyWrite(dirfd int, name string) error {
	if g == nil {
		return nil
	}
	real, err := fdPath(dirfd)
	if err != nil {
		return err
	}
	t := filepath.Join(real, name)
	for _, r := range g.ro {
		if beneath(r, t) {
			return errReadonly
		}
	}
	return nil
}

// denyTree refuses removing name (and everything below) when a readonly mount
// lies in or at the tree.
func (g *guard) denyTree(dirfd int, name string) error {
	if g == nil {
		return nil
	}
	real, err := fdPath(dirfd)
	if err != nil {
		return err
	}
	t := filepath.Join(real, name)
	for _, r := range g.ro {
		if beneath(t, r) {
			return errReadonly
		}
	}
	return nil
}

// denyDirFd refuses a directory that is, contains or lies in a readonly mount.
func (g *guard) denyDirFd(fd int) error {
	if g == nil {
		return nil
	}
	real, err := fdPath(fd)
	if err != nil {
		return err
	}
	for _, r := range g.ro {
		if beneath(r, real) || beneath(real, r) {
			return errReadonly
		}
	}
	return nil
}
