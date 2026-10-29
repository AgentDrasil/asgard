package runnerfs

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
)

// Directory traversal never resolves path strings: a directory is opened
// beneath its root fd, its entries are read with getdents on that fd, each
// entry is classified with fstatat on the same fd, and a symlink is followed
// only through openat2 RESOLVE_BENEATH from the root fd, so a link that leaves
// the root is treated as dangling.

// maxWalkDepth bounds the search recursion.
const maxWalkDepth = 128

// readDirNames returns the names in directory fd (without "." and ".."). It
// consumes the fd's read offset, so the fd must not be shared.
func readDirNames(fd int) ([]string, error) {
	buf := make([]byte, 64<<10)
	var names []string
	for {
		n, err := unix.ReadDirent(fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, mapErrno(err)
		}
		if n <= 0 {
			return names, nil
		}
		_, _, names = unix.ParseDirent(buf[:n], -1, names)
	}
}

// entryInfo is a classified directory entry. For a symlink whose target
// resolves beneath the root, isDir/size/dev/ino describe the target.
type entryInfo struct {
	isDir    bool
	isLink   bool
	resolved bool // symlink target resolved beneath the root
	size     int64
	dev, ino uint64
}

// classify stats name inside dfd without following it; a symlink is then
// resolved beneath rootfd at rootRel (the path of dfd relative to rootfd).
func classify(dfd, rootfd int, rootRel, name string) (entryInfo, bool) {
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return entryInfo{}, false
	}
	e := entryInfo{size: st.Size, dev: uint64(st.Dev), ino: uint64(st.Ino)}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		e.isDir = true
	case unix.S_IFLNK:
		e.isLink = true
		fd, err := openBeneath(rootfd, path.Join(filepath.ToSlash(rootRel), name), unix.O_PATH, 0)
		if err != nil {
			return e, true
		}
		var tst unix.Stat_t
		err = unix.Fstat(fd, &tst)
		_ = unix.Close(fd)
		if err == nil {
			e.resolved = true
			e.isDir = tst.Mode&unix.S_IFMT == unix.S_IFDIR
			e.size, e.dev, e.ino = tst.Size, uint64(tst.Dev), uint64(tst.Ino)
		}
	}
	return e, true
}

// runDirIsBase reports whether the chat's run dir is the namespace dir itself
// (cosmetic: it decides whether display paths carry the /tmp prefix).
func (l Local) runDirIsBase(runDir, ns string) bool {
	ws := filepath.Clean(agents.NormalizeSessionRunDir(runDir, l.ChatID))
	base := filepath.Clean(l.base(ns))
	if ws == base {
		return true
	}
	ews, e1 := filepath.EvalSymlinks(ws)
	eb, e2 := filepath.EvalSymlinks(base)
	return e1 == nil && e2 == nil && ews == eb
}

func rank(isDir bool) int {
	if isDir {
		return 0
	}
	return 1
}

// afterToken reports whether an entry sorts after the page token (kind "d" or
// "f" plus the name).
func afterToken(isDir bool, name, tok string) bool {
	if tok == "" {
		return true
	}
	tokDir := tok[0] == 'd'
	tokName := tok[1:]
	if rank(isDir) != rank(tokDir) {
		return rank(isDir) > rank(tokDir)
	}
	return name > tokName
}

func tokenOf(isDir bool, name string) string {
	if isDir {
		return "d" + name
	}
	return "f" + name
}

func validToken(tok string) bool {
	return tok == "" || (len(tok) >= 2 && (tok[0] == 'd' || tok[0] == 'f'))
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	return min(n, max)
}

// Tree implements DirOps.
func (l Local) Tree(ctx context.Context, req TreeRequest) (TreeResult, error) {
	if err := l.checkChat(); err != nil {
		return TreeResult{}, err
	}
	if !validToken(req.PageToken) {
		return TreeResult{}, Errf(CodeBadRequest, "invalid page token")
	}
	rootfd, rel, display, err := l.resolvePath(req.Path, req.Scope, req.RunDir, false, false)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return TreeResult{}, Errf(CodeNotFound, "directory not found")
		}
		return TreeResult{}, err
	}
	defer func() { _ = unix.Close(rootfd) }()

	info, err := statBeneath(rootfd, rel)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			return TreeResult{}, Errf(CodeNotFound, "directory not found")
		}
		return TreeResult{}, err
	}
	if !info.IsDir() {
		return TreeResult{}, Errf(CodeBadRequest, "requested path is a file, not a directory")
	}
	dfd, err := openBeneath(rootfd, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return TreeResult{}, err
	}
	defer func() { _ = unix.Close(dfd) }()
	names, err := readDirNames(dfd)
	if err != nil {
		return TreeResult{}, err
	}

	relPath := l.treeDisplayPath(req, rel, display)
	items := make([]TreeEntry, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return TreeResult{}, Errf(CodeUnavailable, "request canceled")
		}
		if name == ".git" || name == "node_modules" {
			continue
		}
		e, ok := classify(dfd, rootfd, rel, name)
		if !ok {
			continue
		}
		te := TreeEntry{Name: name, Path: name, IsDir: e.isDir}
		if relPath != "" {
			te.Path = path.Join(filepath.ToSlash(relPath), name)
		}
		if !e.isDir {
			te.Size = e.size
			te.Ext = strings.TrimPrefix(filepath.Ext(name), ".")
		}
		items = append(items, te)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})

	limit := clampLimit(req.Limit, DefaultTreeLimit, MaxTreeLimit)
	start := 0
	for start < len(items) && !afterToken(items[start].IsDir, items[start].Name, req.PageToken) {
		start++
	}
	res := TreeResult{Path: relPath, Entries: []TreeEntry{}}
	end := min(start+limit, len(items))
	res.Entries = append(res.Entries, items[start:end]...)
	if end < len(items) {
		res.NextPageToken = tokenOf(items[end-1].IsDir, items[end-1].Name)
	}
	return res, nil
}

// treeDisplayPath is the response path: relative to the workspace, or
// /tmp, /session prefixed when the listed dir is a namespace dir that is not
// the run dir itself.
func (l Local) treeDisplayPath(req TreeRequest, rel, display string) string {
	h := Header{Path: display}
	if ns := h.Namespace(); ns != "" {
		if l.runDirIsBase(req.RunDir, ns) {
			if rel == "." {
				return ""
			}
			return filepath.ToSlash(rel)
		}
		return display
	}
	if filepath.IsAbs(display) {
		return filepath.Clean(display)
	}
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// overlaps reports whether either real path contains the other.
func overlaps(a, b string) bool { return beneath(a, b) || beneath(b, a) }

type searcher struct {
	ctx     context.Context
	rootfd  int
	scope   string
	prefix  string
	query   string
	max     int
	visited map[[2]uint64]bool
	out     []SearchHit
}

// walk scans directory dfd (path rel below rootfd) in lexical order. It
// returns true when the scan must stop (limit reached or canceled).
func (s *searcher) walk(dfd int, rel string, depth int) bool {
	names, err := readDirNames(dfd)
	if err != nil {
		return false
	}
	sort.Strings(names)
	for _, name := range names {
		if s.ctx.Err() != nil {
			return true
		}
		e, ok := classify(dfd, s.rootfd, rel, name)
		if !ok {
			continue
		}
		if e.isLink {
			if !e.resolved || e.isDir {
				continue
			}
		} else if e.isDir {
			if name == ".git" || name == "node_modules" || s.scope == "session" && name == "workflows" {
				continue
			}
			if depth >= maxWalkDepth {
				continue
			}
			cfd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			stop := s.walk(cfd, path.Join(rel, name), depth+1)
			_ = unix.Close(cfd)
			if stop {
				return true
			}
			continue
		}
		if s.scope == "session" && name == "messages.jsonl" {
			continue
		}
		key := [2]uint64{e.dev, e.ino}
		if s.visited[key] {
			continue
		}
		s.visited[key] = true
		full := path.Join(rel, name)
		if s.prefix != "" {
			full = path.Join(s.prefix, full)
		}
		if s.query == "" || strings.Contains(strings.ToLower(full), s.query) {
			s.out = append(s.out, SearchHit{
				Path: full, Name: name, Ext: strings.TrimPrefix(filepath.Ext(name), "."),
				Size: e.size, Scope: s.scope,
			})
			if len(s.out) >= s.max {
				return true
			}
		}
	}
	return false
}

// scan searches the directory rootfd (not closed here) and appends to out.
func scan(ctx context.Context, rootfd int, scope, prefix, query string, max int, visited map[[2]uint64]bool) []SearchHit {
	if max <= 0 {
		return nil
	}
	dfd, err := unix.Dup(rootfd)
	if err != nil {
		return nil
	}
	defer func() { _ = unix.Close(dfd) }()
	s := &searcher{ctx: ctx, rootfd: rootfd, scope: scope, prefix: prefix, query: query, max: max, visited: visited}
	s.walk(dfd, ".", 0)
	return s.out
}

// Search implements DirOps.
func (l Local) Search(ctx context.Context, req SearchRequest) ([]SearchHit, error) {
	if err := l.checkChat(); err != nil {
		return nil, err
	}
	limit := clampLimit(req.Limit, DefaultSearchLimit, MaxSearchLimit)
	query := strings.ToLower(req.Query)
	visited := make(map[[2]uint64]bool)

	var wsfd = -1
	wsReal := ""
	if fd, _, err := l.openWorkspace(req.RunDir, false); err == nil {
		wsfd = fd
		defer func() { _ = unix.Close(wsfd) }()
		if wsReal, err = fdPath(wsfd); err != nil {
			return nil, err
		}
	} else if CodeOf(err) != CodeNotFound {
		return nil, err
	}

	openNs := func(ns string) (int, string, bool) {
		fd, err := l.namespaceRoot(ns, false)
		if err != nil {
			return -1, "", false
		}
		real, err := fdPath(fd)
		if err != nil {
			_ = unix.Close(fd)
			return -1, "", false
		}
		return fd, real, true
	}

	results := []SearchHit{}
	tmpfd, tmpReal, tmpOK := openNs("tmp")
	if tmpOK {
		defer func() { _ = unix.Close(tmpfd) }()
	}
	switch {
	case wsfd >= 0 && tmpOK && overlaps(wsReal, tmpReal):
		// Overlapping dirs: scan the workspace once to avoid duplicates.
		results = append(results, scan(ctx, wsfd, "workspace", "", query, limit, visited)...)
	default:
		wsLimit := limit
		if tmpOK {
			wsLimit = limit - limit/4
		}
		if wsfd >= 0 {
			results = append(results, scan(ctx, wsfd, "workspace", "", query, wsLimit, visited)...)
		}
		if tmpOK && limit-len(results) > 0 {
			results = append(results, scan(ctx, tmpfd, "tmp", "/tmp", query, limit-len(results), visited)...)
		}
	}
	if sessfd, sessReal, ok := openNs("session"); ok {
		defer func() { _ = unix.Close(sessfd) }()
		if (wsfd < 0 || !overlaps(wsReal, sessReal)) && limit-len(results) > 0 {
			results = append(results, scan(ctx, sessfd, "session", "/session", query, limit-len(results), visited)...)
		}
	}
	return results, nil
}

// subdirRoots are the directories Subdirs may list: the allowed run dirs
// (readonly mounts included). Without an allowlist (development) it is nil.
func (l Local) subdirRoots() []string {
	var roots []string
	for _, d := range l.AllowedRunDirs(false) {
		if filepath.IsAbs(d) {
			roots = append(roots, filepath.Clean(d))
		}
	}
	return roots
}

// openDirAbs opens the absolute directory p: beneath the roots when
// confined, plainly in development.
func (l Local) openDirAbs(roots []string, p string) (int, error) {
	if l.AllowedRunDirs == nil {
		return openRoot(p)
	}
	fd, _, err := l.openBeneathRoots(roots, p, true)
	return fd, err
}

// Subdirs implements DirOps.
func (l Local) Subdirs(ctx context.Context, req SubdirsRequest) (SubdirsResult, error) {
	if !validToken(req.PageToken) {
		return SubdirsResult{}, Errf(CodeBadRequest, "invalid page token")
	}
	dir := filepath.Clean(req.Dir)
	if req.Dir == "" || !filepath.IsAbs(dir) {
		return SubdirsResult{}, Errf(CodeBadRequest, "directory %q is not an absolute path", req.Dir)
	}
	var roots []string
	if l.AllowedRunDirs != nil {
		roots = l.subdirRoots()
	}
	// Open as root fd + rel when confined so symlinked subdirectories resolve
	// beneath the whole root, not only beneath the listed directory.
	var rootfd int
	rel := "."
	if l.AllowedRunDirs == nil {
		fd, err := openRoot(dir)
		if err != nil {
			return SubdirsResult{}, subdirOpenErr(dir, err)
		}
		rootfd = fd
	} else {
		fd, r, err := l.openBeneathRoots(roots, dir, false)
		if err != nil {
			return SubdirsResult{}, subdirOpenErr(dir, err)
		}
		rootfd, rel = fd, r
	}
	defer func() { _ = unix.Close(rootfd) }()
	dfd, err := openBeneath(rootfd, rel, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return SubdirsResult{}, subdirOpenErr(dir, err)
	}
	defer func() { _ = unix.Close(dfd) }()
	names, err := readDirNames(dfd)
	if err != nil {
		return SubdirsResult{}, Errf(CodeInternal, "failed to read directory: %v", err)
	}
	sort.Strings(names)
	limit := clampLimit(req.Limit, DefaultSubdirLimit, MaxTreeLimit)
	res := SubdirsResult{Subdirs: []string{}}
	for _, name := range names {
		if ctx.Err() != nil {
			return SubdirsResult{}, Errf(CodeUnavailable, "request canceled")
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		if req.PageToken != "" && name <= req.PageToken[1:] {
			continue
		}
		if e, ok := classify(dfd, rootfd, rel, name); !ok || !e.isDir {
			continue
		}
		if len(res.Subdirs) == limit {
			res.NextPageToken = "d" + res.Subdirs[len(res.Subdirs)-1]
			break
		}
		res.Subdirs = append(res.Subdirs, name)
	}
	if req.PageToken == "" {
		res.GitRoot = l.gitRootPath(dir, roots)
	}
	return res, nil
}

func subdirOpenErr(dir string, err error) error {
	switch CodeOf(err) {
	case CodeNotFound:
		return Errf(CodeBadRequest, "directory %q does not exist", dir)
	case CodeBadRequest:
		return Errf(CodeBadRequest, "%q is not a directory", dir)
	}
	return err
}

// OpenWorkDir opens runDir (an allowed workspace, tmp or session dir) for use
// as a process working directory: the returned directory fd was opened beneath
// an allowed root, so /proc/self/fd/<fd> names exactly that directory however
// the path is swapped afterwards. The caller closes it.
func (l Local) OpenWorkDir(runDir string) (*os.File, error) {
	if err := l.checkChat(); err != nil {
		return nil, err
	}
	fd, _, err := l.openWorkspace(runDir, false)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "workdir"), nil
}
