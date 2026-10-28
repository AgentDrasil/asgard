package runnerfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
)

// Local is an FS over this host's own layout (pkg/paths). Every access is
// anchored at a root directory fd and resolved beneath it (see confined.go).
type Local struct {
	ChatID string
	// AllowedRunDirs returns the directories a chat's workspace (run dir) and
	// absolute paths may lie in, besides the chat's own tmp/session dirs: the
	// runner passes the run_dirs and mount_dirs of its agents and workflows
	// (readonly mounts only when write is false). Nil means unconfined run
	// dirs and is meant for tests and runner-less development only; the
	// runner always sets it.
	AllowedRunDirs func(write bool) []string
	// ReadonlyDirs lists the readonly mount roots: they veto writes even when
	// a writable root contains them (bwrap's --ro-bind over a --bind).
	ReadonlyDirs func() []string
}

// NewLocal returns the Local FS of chatID with unrestricted run dirs (tests
// and development without a runner).
func NewLocal(chatID string) Local { return Local{ChatID: chatID} }

// NewConfined returns a Local FS whose paths must lie beneath an allowed root.
func NewConfined(chatID string, allowed func(write bool) []string, readonly func() []string) Local {
	if allowed == nil {
		allowed = func(bool) []string { return nil }
	}
	return Local{ChatID: chatID, AllowedRunDirs: allowed, ReadonlyDirs: readonly}
}

var _ FS = Local{}

// testHookBeforeRunDirOpen runs between the lexical check of a run dir and
// its fd-anchored open (tests swap path components there).
var testHookBeforeRunDirOpen func()

func (l Local) checkChat() error {
	if !agents.IsValidChatID(l.ChatID) {
		return Errf(CodeBadRequest, "invalid session id")
	}
	return nil
}

func (l Local) base(ns string) string { return agents.GetSessionScopedBaseDir(ns, l.ChatID) }

// roots lists the absolute directories paths may lie beneath: the chat's own
// tmp/session dirs and the allowed run dirs. The "/tmp"-style tokens of
// IsAllowedDir are not expanded to the shared host roots, which would reach
// other chats' data; the chat's own dirs are always roots already.
func (l Local) roots(write bool, extra string) []string {
	roots := []string{l.base("tmp"), l.base("session")}
	if l.AllowedRunDirs == nil {
		if extra != "" {
			roots = append(roots, extra)
		}
		return roots
	}
	for _, d := range l.AllowedRunDirs(write) {
		if filepath.IsAbs(d) {
			roots = append(roots, filepath.Clean(d))
		}
	}
	return roots
}

// lexicalRel returns target relative to root when it lies inside.
func lexicalRel(root, target string) (string, bool) {
	r, err := filepath.Rel(root, target)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return r, true
}

func (l Local) guard() *guard {
	if l.ReadonlyDirs == nil {
		return nil
	}
	return newGuard(l.ReadonlyDirs())
}

// rootFd opens a root dir: strictly (no symlink in any component) when
// confined, plainly in dev mode.
func (l Local) rootFd(root string) (int, error) {
	if l.AllowedRunDirs == nil {
		return openRoot(root)
	}
	return openRootStrict(root)
}

// openBeneathRoots opens target (absolute) beneath the OUTERMOST allowed root
// containing it. The root is the trust anchor (opened without following
// symlinks); everything below it is resolved by the kernel beneath the root
// fd, so a component swapped for a symlink cannot lead out, and a failure is
// final: it never falls through to a nested root. dir selects directory opens
// (the fd is the directory itself, rel "."); otherwise the root fd and the
// path relative to it are returned.
func (l Local) openBeneathRoots(roots []string, target string, dir bool) (int, string, error) {
	var cands []string
	for _, r := range roots {
		if _, ok := lexicalRel(r, target); ok {
			cands = append(cands, r)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return len(cands[i]) < len(cands[j]) })
	for _, root := range cands {
		rel, _ := lexicalRel(root, target)
		rfd, err := l.rootFd(root)
		if err != nil {
			if CodeOf(err) == CodeNotFound {
				continue
			}
			return -1, "", err
		}
		if !dir {
			return rfd, rel, nil
		}
		if testHookBeforeRunDirOpen != nil {
			testHookBeforeRunDirOpen()
		}
		fd, err := openBeneath(rfd, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		_ = unix.Close(rfd)
		if err != nil {
			return -1, "", err
		}
		return fd, ".", nil
	}
	return -1, "", Errf(CodeDenied, "access denied: path is not beneath an allowed directory")
}

// openWorkspace returns an fd for the chat's run dir and its normalized path.
// Without an allowlist (dev mode) an empty run dir falls back to the working
// directory.
func (l Local) openWorkspace(runDir string, write bool) (int, string, error) {
	if l.AllowedRunDirs == nil {
		ws := runDir
		if ws == "" {
			wd, err := os.Getwd()
			if err != nil {
				return -1, "", Errf(CodeInternal, "failed to determine workspace directory")
			}
			ws = wd
		} else {
			ws = agents.NormalizeSessionRunDir(runDir, l.ChatID)
		}
		fd, err := openRoot(ws)
		return fd, ws, err
	}
	if runDir == "" {
		return -1, "", Errf(CodeBadRequest, "run dir is required")
	}
	if !filepath.IsAbs(runDir) && !isNamespaceForm(runDir) {
		return -1, "", Errf(CodeBadRequest, "run dir must be absolute")
	}
	ws := agents.NormalizeSessionRunDir(runDir, l.ChatID)
	if !filepath.IsAbs(ws) {
		return -1, "", Errf(CodeBadRequest, "run dir must be absolute")
	}
	fd, _, err := l.openBeneathRoots(l.roots(write, ""), ws, true)
	if err != nil {
		return -1, "", err
	}
	return fd, ws, nil
}

func isNamespaceForm(p string) bool {
	c := filepath.Clean(p)
	for _, ns := range []string{"tmp", "session"} {
		if c == ns || strings.HasPrefix(c, ns+"/") || c == "."+ns || strings.HasPrefix(c, "."+ns+"/") {
			return true
		}
	}
	return false
}

// namespaceRoot opens the chat's tmp or session dir.
func (l Local) namespaceRoot(ns string, create bool) (int, error) {
	if create {
		if err := os.MkdirAll(l.base(ns), 0o755); err != nil {
			return -1, Errf(CodeInternal, "failed to create directory: %v", err)
		}
	}
	return l.rootFd(l.base(ns))
}

// resolvePath is the one resolution used by every op (reads and mutations):
// explicit or scoped namespace paths address the chat's tmp/session dir
// without a workspace, absolute paths lie beneath any allowed root, and the
// rest goes through agents.ResolveAndValidatePath in the run dir. It returns
// an open root fd (the caller closes it), the path relative to it and the
// display path.
func (l Local) resolvePath(reqPath, scope, runDir string, write, create bool) (rootfd int, rel, display string, err error) {
	err = nil

	cands := agents.AuthCandidates(reqPath, scope, l.ChatID)
	clean := filepath.Clean(reqPath)
	switch {
	case len(cands) == 1 && cands[0].Ns != "":
		// Explicit namespace path: no workspace needed.
		ns := cands[0].Ns
		var sub string
		if ok, s := agents.ResolveSessionScopedPath(clean, l.ChatID, ns); ok {
			sub = s
		} else if ok, s := agents.RelativeScopedPrefixedPath(clean, l.ChatID, ns); ok {
			sub = s
		}
		rootfd, err = l.namespaceRoot(ns, create)
		rel, display = filepath.Join(".", sub), cands[0].Path
	case filepath.IsAbs(clean) && scope != "tmp" && scope != "session":
		// Absolute path beneath any allowed root.
		rootfd, rel, err = l.openBeneathRoots(l.roots(write, ""), clean, false)
		display = clean
	default:
		wsfd, ws, werr := l.openWorkspace(runDir, write)
		if werr != nil {
			return -1, "", "", werr
		}
		target, ns, authPath, rerr := agents.ResolveAndValidatePath(ws, reqPath, scope, l.ChatID)
		if rerr != nil {
			_ = unix.Close(wsfd)
			if strings.HasPrefix(rerr.Error(), "access denied") {
				return -1, "", "", Errf(CodeDenied, "%s", rerr.Error())
			}
			return -1, "", "", Errf(CodeBadRequest, "%s", rerr.Error())
		}
		display = reqPath
		if ns != "" {
			_ = unix.Close(wsfd)
			display = authPath
			rootfd, err = l.namespaceRoot(ns, create)
			if r, ok := lexicalRel(l.base(ns), target); ok {
				rel = r
			} else {
				err = Errf(CodeDenied, "access denied: path escapes the chat's directories")
			}
		} else {
			rootfd = wsfd
			if r, ok := lexicalRel(ws, target); ok {
				rel = r
			} else {
				err = Errf(CodeDenied, "access denied: path escapes workspace boundary")
			}
		}
	}
	if err != nil {
		if rootfd > 0 {
			_ = unix.Close(rootfd)
		}
		return -1, "", "", err
	}
	return rootfd, rel, display, nil
}

// Open implements FS.
func (l Local) Open(_ context.Context, req ReadRequest) (*File, error) {
	if err := l.checkChat(); err != nil {
		return nil, err
	}
	rootfd, rel, display, err := l.resolvePath(req.Path, req.Scope, req.RunDir, false, false)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil, Errf(CodeNotFound, "file not found: %s", req.Path)
		}
		return nil, err
	}
	name := filepath.Base(rel)
	defer func() { _ = unix.Close(rootfd) }()
	f, info, err := openRegular(rootfd, rel)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil, Errf(CodeNotFound, "file not found: %s", req.Path)
		}
		return nil, err
	}
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	h := Header{
		Path:    display,
		Name:    name,
		Ext:     ext,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Mime:    DetectMimeType(ext, nil),
	}
	if req.Raw {
		h.IsBinary = IsMediaExt(ext)
	} else {
		h.IsBinary = IsMediaExt(ext) || sniffBinary(f)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, Errf(CodeInternal, "failed to probe file: %v", err)
		}
	}
	return &File{Header: h, ReadSeekCloser: f}, nil
}

// ValidAttachmentName reports whether filename is a plain file name.
func ValidAttachmentName(filename string) bool {
	return filename != "" && len(filename) <= 255 && filename != "." && filename != ".." &&
		filepath.Base(filename) == filename && !strings.ContainsAny(filename, "/\\\x00")
}

// OpenAttachment implements FS.
func (l Local) OpenAttachment(_ context.Context, filename string) (*File, error) {
	if err := l.checkChat(); err != nil {
		return nil, err
	}
	if !ValidAttachmentName(filename) {
		return nil, Errf(CodeBadRequest, "invalid filename")
	}
	rootfd, err := l.rootFd(l.base("tmp"))
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil, Errf(CodeNotFound, "attachment not found")
		}
		return nil, err
	}
	defer func() { _ = unix.Close(rootfd) }()
	f, info, err := openRegular(rootfd, filepath.Join("attachments", filename))
	if err != nil {
		switch CodeOf(err) {
		case CodeNotFound:
			return nil, Errf(CodeNotFound, "attachment not found")
		case CodeIsDir:
			return nil, Errf(CodeBadRequest, "requested path is a directory")
		}
		return nil, err
	}
	ext := filepath.Ext(filename)
	return &File{
		Header: Header{
			Path: filename, Name: filename, Ext: strings.TrimPrefix(ext, "."),
			Size: info.Size(), ModTime: info.ModTime(), Mime: DetectMimeType(ext, nil),
		},
		ReadSeekCloser: f,
	}, nil
}

// maxDedupeRetries bounds the name search for a free attachment name.
const maxDedupeRetries = 100

// PutAttachment implements FS. Retries with the same requestID are made
// idempotent by the runner service, not here. The filename is validated here
// too: the runner never trusts the backend's sanitizing.
func (l Local) PutAttachment(_ context.Context, _ string, filename string, r io.Reader) (PutResult, error) {
	if err := l.checkChat(); err != nil {
		return PutResult{}, err
	}
	if !ValidAttachmentName(filename) {
		return PutResult{}, Errf(CodeBadRequest, "invalid filename")
	}
	if err := os.MkdirAll(l.base("tmp"), 0o755); err != nil {
		return PutResult{}, Errf(CodeInternal, "failed to create storage directory: %v", err)
	}
	rootfd, err := l.rootFd(l.base("tmp"))
	if err != nil {
		return PutResult{}, err
	}
	defer func() { _ = unix.Close(rootfd) }()
	dirfd, err := mkdirAllBeneath(rootfd, "attachments", true, nil)
	if err != nil {
		return PutResult{}, err
	}
	defer func() { _ = unix.Close(dirfd) }()

	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	fd := -1
	var name string
	for attempt := 0; attempt < maxDedupeRetries; attempt++ {
		name = filename
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, attempt, ext)
		}
		fd, err = unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err == nil {
			break
		}
		fd = -1
		if !errors.Is(err, unix.EEXIST) {
			return PutResult{}, Errf(CodeInternal, "failed to create attachment file: %v", err)
		}
	}
	if fd < 0 {
		return PutResult{}, Errf(CodeInternal, "too many filename collisions")
	}
	f := os.NewFile(uintptr(fd), name)
	n, err := io.Copy(f, io.LimitReader(r, MaxAttachmentSize+1))
	_ = f.Close()
	if err == nil && n > MaxAttachmentSize {
		err = Errf(CodeTooLarge, "file size exceeds 20MB limit")
	}
	if err != nil {
		_ = unix.Unlinkat(dirfd, name, 0)
		if CodeOf(err) == CodeTooLarge {
			return PutResult{}, err
		}
		return PutResult{}, Errf(CodeInternal, "failed to save attachment: %v", err)
	}
	return PutResult{Name: name, Size: n}, nil
}

// resolveRef maps a PathRef onto an open root fd and a path relative to it
// with the same resolution as Open. The caller closes rootfd. create makes a
// missing namespace root.
func (l Local) resolveRef(ref PathRef, create, write bool) (int, string, error) {
	if err := l.checkChat(); err != nil {
		return -1, "", err
	}
	if ref.Path == "" {
		return -1, "", Errf(CodeBadRequest, "path is required")
	}
	switch ref.Scope {
	case "", "workspace", "tmp", "session":
	default:
		return -1, "", Errf(CodeBadRequest, "invalid scope %q", ref.Scope)
	}
	fd, rel, _, err := l.resolvePath(ref.Path, ref.Scope, ref.RunDir, write, create)
	if err != nil {
		return -1, "", err
	}
	if rel == "." {
		_ = unix.Close(fd)
		return -1, "", Errf(CodeBadRequest, "path must name a file below the root")
	}
	return fd, rel, nil
}

// Stat implements FS.
func (l Local) Stat(_ context.Context, ref PathRef) (StatResult, error) {
	rootfd, rel, err := l.resolveRef(ref, false, false)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return StatResult{}, nil
		}
		return StatResult{}, err
	}
	defer func() { _ = unix.Close(rootfd) }()
	info, err := statBeneath(rootfd, rel)
	if CodeOf(err) == CodeNotFound {
		return StatResult{}, nil
	}
	if err != nil {
		return StatResult{}, err
	}
	return StatResult{Exists: true, IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()}, nil
}

// WriteFile implements FS.
func (l Local) WriteFile(_ context.Context, req WriteRequest, r io.Reader) (int64, error) {
	rootfd, rel, err := l.resolveRef(req.Ref, req.Mkdir, true)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(rootfd) }()
	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}
	if req.SkipIfUnchanged {
		data, err := io.ReadAll(&capReader{r: r, left: MaxWriteSize, msg: "write exceeds the maximum size"})
		if err != nil {
			if CodeOf(err) == CodeTooLarge {
				return 0, err
			}
			return 0, Errf(CodeInternal, "failed to read data: %v", err)
		}
		if f, _, oerr := openRegular(rootfd, rel); oerr == nil {
			old, rerr := io.ReadAll(io.LimitReader(f, MaxWriteSize+1))
			_ = f.Close()
			if rerr == nil && bytes.Equal(old, data) {
				return int64(len(data)), nil
			}
		}
		r = bytes.NewReader(data)
	}
	return writeBeneath(rootfd, rel, req, mode, r, l.guard())
}

// AppendFile implements FS.
func (l Local) AppendFile(_ context.Context, req AppendRequest) error {
	if len(req.Data) > MaxWriteSize {
		return Errf(CodeTooLarge, "append exceeds %d bytes", MaxWriteSize)
	}
	rootfd, rel, err := l.resolveRef(req.Ref, req.Mkdir, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootfd) }()
	return appendBeneath(rootfd, rel, req.Mkdir, req.Data, req.Sync, l.guard())
}

// Remove implements FS.
func (l Local) Remove(_ context.Context, ref PathRef, recursive bool, _ string) error {
	rootfd, rel, err := l.resolveRef(ref, false, true)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return nil
		}
		return err
	}
	defer func() { _ = unix.Close(rootfd) }()
	return removeBeneath(rootfd, rel, recursive, l.guard())
}
