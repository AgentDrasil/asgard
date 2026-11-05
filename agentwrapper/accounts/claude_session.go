package accounts

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// ClaudeCredentialsFile is the claude oauth credentials file inside an account dir.
const ClaudeCredentialsFile = ".credentials.json"

var sessionIDRegex = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// ValidateSessionID checks that a claude session id is uuid-like.
func ValidateSessionID(sid string) error {
	if !sessionIDRegex.MatchString(sid) {
		return fmt.Errorf("invalid session id %q: must match ^[A-Za-z0-9-]{8,64}$", sid)
	}
	return nil
}

// regularFile returns the file info of p when it is a regular file (symlinks
// are not followed and count as absent).
func regularFile(p string) (fs.FileInfo, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	return fi, true
}

func realDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// projectDirs lists the real (non-symlink) project directories of an account dir.
func projectDirs(accountDir string) []string {
	root := filepath.Join(accountDir, "projects")
	if !realDir(root) {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			out = append(out, e.Name())
		}
	}
	return out
}

// sessionCopy is the newest-copy candidate of a session within one account.
type sessionCopy struct {
	size  int64
	mtime time.Time
}

// FindClaudeSession returns the claude account alias holding the newest copy of
// projects/*/<sessionID>.jsonl. The jsonl is append-only, so the largest file
// wins; ties are broken by newest mtime, then by alias sort order.
func FindClaudeSession(sessionID string) (alias string, ok bool, err error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", false, err
	}
	aliases, err := List("claude")
	if err != nil {
		return "", false, err
	}
	var best sessionCopy
	for _, a := range aliases {
		dir, err := Dir("claude", a)
		if err != nil {
			return "", false, err
		}
		for _, proj := range projectDirs(dir) {
			fi, isReg := regularFile(filepath.Join(dir, "projects", proj, sessionID+".jsonl"))
			if !isReg {
				continue
			}
			c := sessionCopy{size: fi.Size(), mtime: fi.ModTime()}
			if !ok || c.size > best.size || (c.size == best.size && c.mtime.After(best.mtime)) {
				alias, ok, best = a, true, c
			}
		}
	}
	return alias, ok, nil
}

// CopyClaudeSession copies the session file family of sessionID from the
// claude account srcAlias to dstAlias: projects/<proj>/<sid>.jsonl and
// projects/<proj>/<sid>/, file-history/<sid>/, session-env/<sid>/ and
// todos/<sid>-*.json. Credentials, .claude.json and sessions/ are never copied.
// A destination jsonl that is at least as large as the source is kept.
// The jsonl files are copied last so an interrupted copy never leaves a jsonl
// that would make a retry skip the ancillary files.
func CopyClaudeSession(srcAlias, dstAlias, sessionID string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	if srcAlias == dstAlias {
		return fmt.Errorf("source and destination account are both %q", srcAlias)
	}
	src, err := Dir("claude", srcAlias)
	if err != nil {
		return err
	}
	dst, err := Dir("claude", dstAlias)
	if err != nil {
		return err
	}
	if !realDir(src) {
		return fmt.Errorf("claude account %q does not exist", srcAlias)
	}
	if !realDir(dst) {
		return fmt.Errorf("claude account %q does not exist", dstAlias)
	}

	var jsonlProjs []string
	for _, proj := range projectDirs(src) {
		if _, ok := regularFile(filepath.Join(src, "projects", proj, sessionID+".jsonl")); ok {
			jsonlProjs = append(jsonlProjs, proj)
		}
	}
	if len(jsonlProjs) == 0 {
		return fmt.Errorf("session %s not found in claude account %q", sessionID, srcAlias)
	}

	// Ancillary files first.
	for _, proj := range projectDirs(src) {
		sd := filepath.Join(src, "projects", proj, sessionID)
		if realDir(sd) {
			if err := copyTree(sd, dst, []string{"projects", proj, sessionID}); err != nil {
				return err
			}
		}
	}
	for _, d := range []string{"file-history", "session-env"} {
		if realDir(filepath.Join(src, d)) && realDir(filepath.Join(src, d, sessionID)) {
			if err := copyTree(filepath.Join(src, d, sessionID), dst, []string{d, sessionID}); err != nil {
				return err
			}
		}
	}
	if realDir(filepath.Join(src, "todos")) {
		matches, err := filepath.Glob(filepath.Join(src, "todos", sessionID+"-*.json"))
		if err != nil {
			return err
		}
		for _, m := range matches {
			if err := copySessionFile(m, dst, []string{"todos", filepath.Base(m)}); err != nil {
				return err
			}
		}
	}
	// The jsonl files last.
	for _, proj := range jsonlProjs {
		name := sessionID + ".jsonl"
		if err := copySessionFile(filepath.Join(src, "projects", proj, name), dst, []string{"projects", proj, name}); err != nil {
			return err
		}
	}
	return nil
}

// ensureRealDirs makes sure root/rel[0]/rel[1]/... exist as real directories.
// Every existing component is Lstat'ed and must be a directory, not a symlink;
// missing components are created one level at a time (0700).
func ensureRealDirs(root string, rel []string) (string, error) {
	cur := root
	if fi, err := os.Lstat(cur); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a real directory", cur)
	}
	for _, c := range rel {
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		switch {
		case err == nil:
			if !fi.IsDir() {
				return "", fmt.Errorf("refusing to copy into %s: not a real directory", cur)
			}
		case os.IsNotExist(err):
			if err := os.Mkdir(cur, DirMode); err != nil {
				return "", err
			}
		default:
			return "", err
		}
	}
	return cur, nil
}

// openRegular opens p without following symlinks and requires a regular file.
func openRegular(p string) (*os.File, fs.FileInfo, error) {
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), p)
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, nil, err
	}
	return f, fi, nil
}

// copyTree copies the regular files and directories beneath srcDir into
// dstRoot/dstRel, skipping symlinks and special files.
func copyTree(srcDir, dstRoot string, dstRel []string) error {
	if _, err := ensureRealDirs(dstRoot, dstRel); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		rel := append(append([]string(nil), dstRel...), e.Name())
		p := filepath.Join(srcDir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
		case e.IsDir():
			if err := copyTree(p, dstRoot, rel); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := copySessionFile(p, dstRoot, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// copySessionFile atomically copies src to dstRoot/dstRel (0600), keeping an
// existing destination that is at least as large as src. Source symlinks and
// non-regular files are skipped.
func copySessionFile(src, dstRoot string, dstRel []string) error {
	in, sfi, err := openRegular(src)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENXIO) {
			return nil
		}
		if _, isReg := regularFile(src); !isReg {
			return nil
		}
		return err
	}
	defer func() { _ = in.Close() }()
	dir, err := ensureRealDirs(dstRoot, dstRel[:len(dstRel)-1])
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, dstRel[len(dstRel)-1])
	if dfi, err := os.Lstat(dst); err == nil && dfi.Mode().IsRegular() && dfi.Size() >= sfi.Size() {
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func(e error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return e
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(FileMode); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
