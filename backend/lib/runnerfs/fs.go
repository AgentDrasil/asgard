// Package runnerfs is the backend's view of a chat's per-chat directories
// (tmp, session, workspace, attachments). Those directories live on the
// runner the chat is bound to, so the backend reaches them through the FS
// interface: Remote talks to the runner's RunnerService, Local operates on
// this host's own layout (the runner service itself is built on Local, and
// tests and runner-less development use it directly).
package runnerfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Code classifies an Error; the HTTP handlers map it to a status code.
type Code int

const (
	CodeInternal Code = iota
	CodeNotFound
	CodeDenied
	CodeBadRequest
	CodeTooLarge
	CodeIsDir
	// CodeUnavailable means the runner could not be reached.
	CodeUnavailable
)

// Error is a classified filesystem error with a user-facing message.
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Errf builds an Error.
func Errf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// CodeOf returns the Code of err (CodeInternal for unclassified errors).
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// Limits shared by both implementations.
const (
	// MaxAttachmentSize is the largest accepted attachment.
	MaxAttachmentSize = 20 * 1024 * 1024
	// MaxWriteSize caps a single WriteFile/AppendFile payload (the protocol's
	// whole-message cap).
	MaxWriteSize = 12 * 1024 * 1024
	// chunkSize is the data frame size of streamed transfers.
	chunkSize = 256 * 1024
)

// ReadRequest selects a file for reading. Scope is "", "workspace", "tmp" or
// "session" like the HTTP handlers' scope parameter.
type ReadRequest struct {
	// RunDir is the chat's stored run dir (empty: the runner's cwd).
	RunDir string
	Path   string
	Scope  string
	// Raw skips the binary probe (download of media bytes).
	Raw bool
}

// Header describes an opened file.
type Header struct {
	// Path is the display path: the canonical /tmp/... or /session/... form
	// for namespaced files, otherwise the requested path.
	Path     string
	Name     string
	Ext      string
	Size     int64
	IsBinary bool
	ModTime  time.Time
	Mime     string
}

// Namespace returns "tmp" or "session" for namespaced display paths and ""
// for workspace paths.
func (h Header) Namespace() string {
	for _, ns := range []string{"tmp", "session"} {
		if h.Path == "/"+ns || len(h.Path) > len(ns)+2 && h.Path[:len(ns)+2] == "/"+ns+"/" {
			return ns
		}
	}
	return ""
}

// File is an opened file whose total size is Header.Size. Seeking is cheap;
// Remote files fetch byte ranges lazily.
type File struct {
	Header
	io.ReadSeekCloser
}

// PathRef addresses a file for Stat/Write/Append/Remove. Scope is "", "workspace",
// "tmp" or "session"; Path is relative to the scope root or absolute inside an
// allowed root.
type PathRef struct {
	RunDir string
	Path   string
	Scope  string
}

// StatResult is the outcome of Stat.
type StatResult struct {
	Exists  bool
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// WriteRequest describes a WriteFile call.
type WriteRequest struct {
	Ref   PathRef
	Mkdir bool
	// Mode is the permission bits (0 means 0644).
	Mode uint32
	// Atomic writes a temp file and renames it over the target.
	Atomic bool
	// Sync fsyncs (with Atomic: temp file, rename, parent dir).
	Sync            bool
	SkipIfUnchanged bool
	RequestID       string
}

// AppendRequest describes an AppendFile call.
type AppendRequest struct {
	Ref       PathRef
	Mkdir     bool
	Data      []byte
	Sync      bool
	RequestID string
}

// PutResult is the outcome of PutAttachment.
type PutResult struct {
	Name string
	Size int64
}

// FS is the per-chat file access surface.
type FS interface {
	// Open opens a file for reading. The caller must authorize the header
	// (Path/Namespace) before reading and Close the file.
	Open(ctx context.Context, req ReadRequest) (*File, error)
	// OpenAttachment opens an uploaded attachment of the chat.
	OpenAttachment(ctx context.Context, filename string) (*File, error)
	// PutAttachment stores r as a new attachment under a free name derived
	// from filename (already sanitized). requestID makes retries idempotent.
	PutAttachment(ctx context.Context, requestID, filename string, r io.Reader) (PutResult, error)
	Stat(ctx context.Context, ref PathRef) (StatResult, error)
	WriteFile(ctx context.Context, req WriteRequest, r io.Reader) (int64, error)
	AppendFile(ctx context.Context, req AppendRequest) error
	Remove(ctx context.Context, ref PathRef, recursive bool, requestID string) error
	DirOps
}

// TreeRequest lists one directory. Limit 0 means DefaultTreeLimit.
type TreeRequest struct {
	RunDir    string
	Path      string
	Scope     string
	Limit     int
	PageToken string
}

// TreeEntry is one directory entry; Path is relative to the workspace (or
// prefixed /tmp, /session when the namespace is not the run dir itself).
type TreeEntry struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
	Ext   string
}

// TreeResult is one page of a directory listing: directories first, then by
// name. NextPageToken is empty on the last page.
type TreeResult struct {
	Entries       []TreeEntry
	Path          string
	NextPageToken string
}

// SearchRequest searches the workspace, tmp and session dirs by path
// substring. Limit is clamped to 1..MaxSearchLimit (0: DefaultSearchLimit).
type SearchRequest struct {
	RunDir string
	Query  string
	Limit  int
}

// SearchHit is one matching file.
type SearchHit struct {
	Path  string
	Name  string
	Ext   string
	Size  int64
	Scope string // "workspace" | "tmp" | "session"
}

// SubdirsRequest lists the visible subdirectories of an absolute directory.
type SubdirsRequest struct {
	Dir       string
	Limit     int
	PageToken string
}

// SubdirsResult is one page of subdirectory names plus the enclosing git root.
type SubdirsResult struct {
	Subdirs       []string
	GitRoot       string
	NextPageToken string
}

// GitDiffRequest selects the working tree diff (Commit empty) or one commit.
type GitDiffRequest struct {
	RunDir string
	Commit string
}

// GitDiffFile is one changed file. Old/New content are cut at MaxDiffSide
// bytes (ContentTruncated) and the hunks at MaxDiffHunks (HunksTruncated).
type GitDiffFile struct {
	OldPath          string
	NewPath          string
	Status           string
	OldContent       string
	NewContent       string
	Hunks            []string
	ContentTruncated bool
	HunksTruncated   bool
}

// GitCommit is one log entry.
type GitCommit struct {
	Hash, ShortHash, Author, AuthorEmail, Date, RelativeDate, Refs, Message string
}

// GitLogResult is the recent history plus branch state.
type GitLogResult struct {
	Commits        []GitCommit
	CurrentBranch  string
	TrackingBranch string
	Ahead, Behind  int
	UnstashedCount int
}

// GitOpResult is the outcome of push/pull; a failing git command is reported
// here (Success false), not as an error.
type GitOpResult struct {
	Success bool
	Output  string
	Error   string
}

// Limits of the directory and git operations.
const (
	DefaultTreeLimit   = 5000
	MaxTreeLimit       = 10000
	DefaultSearchLimit = 50
	MaxSearchLimit     = 100
	DefaultSubdirLimit = 5000
	// MaxDiffSide caps each side of a file diff; MaxDiffHunks caps the hunks
	// of a file; MaxDiffFrame bounds a whole diff frame (the protocol's
	// 12 MiB whole-message cap, with headroom for paths).
	MaxDiffSide  = 4 << 20
	MaxDiffHunks = 4 << 20
	MaxDiffFrame = 11 << 20
)

// DirOps is the directory listing, search and git surface of a chat.
type DirOps interface {
	Tree(ctx context.Context, req TreeRequest) (TreeResult, error)
	Search(ctx context.Context, req SearchRequest) ([]SearchHit, error)
	// Subdirs needs no chat: dir is an absolute directory on the runner.
	Subdirs(ctx context.Context, req SubdirsRequest) (SubdirsResult, error)
	// GitDiff calls yield once per changed file; a false return stops early.
	GitDiff(ctx context.Context, req GitDiffRequest, yield func(GitDiffFile) bool) error
	GitLog(ctx context.Context, runDir string, limit int) (GitLogResult, error)
	GitPush(ctx context.Context, runDir, requestID string) (GitOpResult, error)
	GitPull(ctx context.Context, runDir, requestID string) (GitOpResult, error)
}
