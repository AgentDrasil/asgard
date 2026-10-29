package runnerfs

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Remote is an FS over a runner's RunnerService.
type Remote struct {
	ChatID string
	Client runnerpb.RunnerServiceClient
}

// NewRemote returns the Remote FS of chatID on the runner behind client.
func NewRemote(client runnerpb.RunnerServiceClient, chatID string) Remote {
	return Remote{ChatID: chatID, Client: client}
}

var _ FS = Remote{}

// ScopeToProto maps the textual scope onto FileScope.
func ScopeToProto(scope string) runnerpb.FileScope {
	switch scope {
	case "workspace":
		return runnerpb.FileScope_FILE_SCOPE_WORKSPACE
	case "tmp":
		return runnerpb.FileScope_FILE_SCOPE_TMP
	case "session":
		return runnerpb.FileScope_FILE_SCOPE_SESSION
	}
	return runnerpb.FileScope_FILE_SCOPE_UNSPECIFIED
}

// ScopeFromProto is the inverse of ScopeToProto.
func ScopeFromProto(s runnerpb.FileScope) string {
	switch s {
	case runnerpb.FileScope_FILE_SCOPE_WORKSPACE:
		return "workspace"
	case runnerpb.FileScope_FILE_SCOPE_TMP:
		return "tmp"
	case runnerpb.FileScope_FILE_SCOPE_SESSION:
		return "session"
	}
	return ""
}

// RunDirRef maps a stored run dir onto the wire like the dispatcher does
// (agents.DirRefFor): the chat's own tmp/session dirs become TMP/SESSION refs.
func RunDirRef(chatID, runDir string) *runnerpb.DirRef { return agents.DirRefFor(chatID, runDir) }

func pathRef(chatID string, ref PathRef) *runnerpb.PathRef {
	return &runnerpb.PathRef{
		SessionId: chatID,
		RunDir:    RunDirRef(chatID, ref.RunDir),
		Scope:     ScopeToProto(ref.Scope),
		Path:      ref.Path,
	}
}

// ToError converts a gRPC error from the runner into an *Error.
func ToError(err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	var fe *Error
	if errors.As(err, &fe) {
		return err
	}
	st, ok := status.FromError(err)
	if !ok {
		return Errf(CodeInternal, "%v", err)
	}
	code := CodeInternal
	switch st.Code() {
	case codes.NotFound:
		code = CodeNotFound
	case codes.PermissionDenied:
		code = CodeDenied
	case codes.InvalidArgument:
		code = CodeBadRequest
	case codes.ResourceExhausted:
		code = CodeTooLarge
	case codes.FailedPrecondition:
		code = CodeIsDir
	case codes.Unavailable, codes.Canceled, codes.DeadlineExceeded:
		code = CodeUnavailable
	}
	return Errf(code, "%s", st.Message())
}

type readStream = grpc.ServerStreamingClient[runnerpb.ReadFileChunk]

// openRanged opens a remote file: open(ctx, offset) starts a stream at a byte
// offset. The first stream (offset 0) delivers the header.
func openRanged(ctx context.Context, open func(ctx context.Context, off int64) (readStream, error)) (*File, error) {
	sctx, cancel := context.WithCancel(ctx)
	st, err := open(sctx, 0)
	if err != nil {
		cancel()
		return nil, ToError(err)
	}
	first, err := st.Recv()
	if err != nil {
		cancel()
		return nil, ToError(err)
	}
	info := first.GetInfo()
	if info == nil {
		cancel()
		return nil, Errf(CodeInternal, "runner sent no file header")
	}
	h := Header{
		Path: info.GetPath(), Name: info.GetName(), Ext: info.GetExt(), Size: info.GetSize(),
		IsBinary: info.GetIsBinary(), Mime: info.GetMime(),
		ModTime: time.Unix(0, info.GetUpdatedAtUnixNano()),
	}
	r := &remoteReader{ctx: ctx, open: open, size: h.Size, cur: st, cancel: cancel}
	return &File{Header: h, ReadSeekCloser: r}, nil
}

// remoteReader is a lazily ranged io.ReadSeekCloser over a ReadFile stream.
type remoteReader struct {
	ctx    context.Context
	open   func(ctx context.Context, off int64) (readStream, error)
	size   int64
	pos    int64
	cur    readStream
	cancel context.CancelFunc
	buf    []byte
}

func (r *remoteReader) drop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.cur, r.cancel, r.buf = nil, nil, nil
}

func (r *remoteReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	for len(r.buf) == 0 {
		if r.cur == nil {
			sctx, cancel := context.WithCancel(r.ctx)
			st, err := r.open(sctx, r.pos)
			if err != nil {
				cancel()
				return 0, ToError(err)
			}
			r.cur, r.cancel = st, cancel
		}
		chunk, err := r.cur.Recv()
		if err != nil {
			r.drop()
			if err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, ToError(err)
		}
		r.buf = chunk.GetData() // info frames carry no data and are skipped
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	r.pos += int64(n)
	return n, nil
}

func (r *remoteReader) Seek(off int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = off
	case io.SeekCurrent:
		abs = r.pos + off
	case io.SeekEnd:
		abs = r.size + off
	default:
		return 0, errors.New("runnerfs: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("runnerfs: negative position")
	}
	if abs != r.pos {
		r.drop()
		r.pos = abs
	}
	return abs, nil
}

func (r *remoteReader) Close() error {
	r.drop()
	return nil
}

// Open implements FS.
func (r Remote) Open(ctx context.Context, req ReadRequest) (*File, error) {
	return openRanged(ctx, func(ctx context.Context, off int64) (readStream, error) {
		return r.Client.ReadFile(ctx, &runnerpb.ReadFileRequest{
			SessionId: r.ChatID, Path: req.Path, Scope: ScopeToProto(req.Scope),
			Raw: req.Raw, RunDir: RunDirRef(r.ChatID, req.RunDir), Offset: off,
		})
	})
}

// OpenAttachment implements FS.
func (r Remote) OpenAttachment(ctx context.Context, filename string) (*File, error) {
	return openRanged(ctx, func(ctx context.Context, off int64) (readStream, error) {
		return r.Client.GetAttachment(ctx, &runnerpb.GetAttachmentRequest{
			SessionId: r.ChatID, Filename: filename, Offset: off,
		})
	})
}

// sendChunks reads src and sends it in chunkSize pieces with send.
func sendChunks(src io.Reader, send func([]byte) error) error {
	buf := make([]byte, chunkSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if serr := send(buf[:n:n]); serr != nil {
				return serr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// PutAttachment implements FS.
func (r Remote) PutAttachment(ctx context.Context, requestID, filename string, src io.Reader) (PutResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := r.Client.PutAttachment(ctx)
	if err != nil {
		return PutResult{}, ToError(err)
	}
	err = st.Send(&runnerpb.PutAttachmentChunk{Payload: &runnerpb.PutAttachmentChunk_Header{
		Header: &runnerpb.AttachmentHeader{SessionId: r.ChatID, Filename: filename, RequestId: requestID},
	}})
	if err == nil {
		err = sendChunks(src, func(b []byte) error {
			return st.Send(&runnerpb.PutAttachmentChunk{Payload: &runnerpb.PutAttachmentChunk_Data{Data: b}})
		})
	}
	if err != nil && err != io.EOF {
		// A local read failure aborts the upload; a server-side rejection
		// (io.EOF on Send) surfaces from CloseAndRecv below.
		return PutResult{}, ToError(err)
	}
	resp, err := st.CloseAndRecv()
	if err != nil {
		return PutResult{}, ToError(err)
	}
	return PutResult{Name: resp.GetFilename(), Size: resp.GetSize()}, nil
}

// Stat implements FS.
func (r Remote) Stat(ctx context.Context, ref PathRef) (StatResult, error) {
	resp, err := r.Client.Stat(ctx, &runnerpb.StatRequest{Ref: pathRef(r.ChatID, ref)})
	if err != nil {
		return StatResult{}, ToError(err)
	}
	return StatResult{
		Exists: resp.GetExists(), IsDir: resp.GetIsDir(), Size: resp.GetSize(),
		ModTime: time.Unix(0, resp.GetUpdatedAtUnixNano()),
	}, nil
}

// WriteFile implements FS.
func (r Remote) WriteFile(ctx context.Context, req WriteRequest, src io.Reader) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := r.Client.WriteFile(ctx)
	if err != nil {
		return 0, ToError(err)
	}
	err = st.Send(&runnerpb.WriteFileChunk{Payload: &runnerpb.WriteFileChunk_Header{Header: &runnerpb.WriteFileHeader{
		Ref: pathRef(r.ChatID, req.Ref), Mkdir: req.Mkdir, Mode: req.Mode, Atomic: req.Atomic,
		Sync: req.Sync, RequestId: req.RequestID, SkipIfUnchanged: req.SkipIfUnchanged,
	}}})
	if err == nil {
		err = sendChunks(src, func(b []byte) error {
			return st.Send(&runnerpb.WriteFileChunk{Payload: &runnerpb.WriteFileChunk_Data{Data: b}})
		})
	}
	if err != nil && err != io.EOF {
		return 0, ToError(err)
	}
	resp, err := st.CloseAndRecv()
	if err != nil {
		return 0, ToError(err)
	}
	return resp.GetSize(), nil
}

// AppendFile implements FS.
func (r Remote) AppendFile(ctx context.Context, req AppendRequest) error {
	_, err := r.Client.AppendFile(ctx, &runnerpb.AppendFileRequest{
		Ref: pathRef(r.ChatID, req.Ref), Mkdir: req.Mkdir, Data: req.Data, Sync: req.Sync, RequestId: req.RequestID,
	})
	return ToError(err)
}

// Remove implements FS.
func (r Remote) Remove(ctx context.Context, ref PathRef, recursive bool, requestID string) error {
	_, err := r.Client.Remove(ctx, &runnerpb.RemoveRequest{
		Ref: pathRef(r.ChatID, ref), Recursive: recursive, RequestId: requestID,
	})
	return ToError(err)
}

// Mkdir implements FS.
func (r Remote) Mkdir(ctx context.Context, ref PathRef, requestID string) error {
	_, err := r.Client.Mkdir(ctx, &runnerpb.MkdirRequest{Ref: pathRef(r.ChatID, ref), RequestId: requestID})
	return ToError(err)
}

var _ DirOps = Remote{}

// Tree implements DirOps. It follows the runner's pages to the end.
func (r Remote) Tree(ctx context.Context, req TreeRequest) (TreeResult, error) {
	res := TreeResult{Entries: []TreeEntry{}}
	token := req.PageToken
	for {
		resp, err := r.Client.Tree(ctx, &runnerpb.TreeRequest{
			SessionId: r.ChatID, Path: req.Path, Scope: ScopeToProto(req.Scope),
			RunDir: RunDirRef(r.ChatID, req.RunDir), Limit: int32(req.Limit), PageToken: token,
		})
		if err != nil {
			return TreeResult{}, ToError(err)
		}
		res.Path = resp.GetPath()
		for _, e := range resp.GetEntries() {
			res.Entries = append(res.Entries, TreeEntry{Name: e.GetName(), Path: e.GetPath(), IsDir: e.GetIsDir(), Size: e.GetSize(), Ext: e.GetExt()})
		}
		token = resp.GetNextPageToken()
		if token == "" || req.Limit > 0 {
			// A caller that asks for a page limit pages itself.
			res.NextPageToken = token
			return res, nil
		}
	}
}

// Search implements DirOps.
func (r Remote) Search(ctx context.Context, req SearchRequest) ([]SearchHit, error) {
	resp, err := r.Client.Search(ctx, &runnerpb.SearchRequest{
		SessionId: r.ChatID, Query: req.Query, Limit: int32(req.Limit), RunDir: RunDirRef(r.ChatID, req.RunDir),
	})
	if err != nil {
		return nil, ToError(err)
	}
	hits := make([]SearchHit, 0, len(resp.GetFiles()))
	for _, f := range resp.GetFiles() {
		hits = append(hits, SearchHit{Path: f.GetPath(), Name: f.GetName(), Ext: f.GetExt(), Size: f.GetSize(), Scope: ScopeFromProto(f.GetScope())})
	}
	return hits, nil
}

// Subdirs implements DirOps. It follows the runner's pages to the end unless
// the caller sets a page limit.
func (r Remote) Subdirs(ctx context.Context, req SubdirsRequest) (SubdirsResult, error) {
	res := SubdirsResult{Subdirs: []string{}}
	token := req.PageToken
	for {
		resp, err := r.Client.Subdirs(ctx, &runnerpb.SubdirsRequest{Dir: req.Dir, Limit: int32(req.Limit), PageToken: token})
		if err != nil {
			return SubdirsResult{}, ToError(err)
		}
		res.Subdirs = append(res.Subdirs, resp.GetSubdirs()...)
		if resp.GetGitRoot() != "" {
			res.GitRoot = resp.GetGitRoot()
		}
		token = resp.GetNextPageToken()
		if token == "" || req.Limit > 0 {
			res.NextPageToken = token
			return res, nil
		}
	}
}

// GitDiff implements DirOps.
func (r Remote) GitDiff(ctx context.Context, req GitDiffRequest, yield func(GitDiffFile) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := r.Client.GitDiff(ctx, &runnerpb.GitDiffRequest{
		SessionId: r.ChatID, Commit: req.Commit, RunDir: RunDirRef(r.ChatID, req.RunDir),
	})
	if err != nil {
		return ToError(err)
	}
	for {
		f, err := st.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ToError(err)
		}
		if !yield(GitDiffFile{
			OldPath: f.GetOldPath(), NewPath: f.GetNewPath(), Status: f.GetStatus(),
			OldContent: f.GetOldContent(), NewContent: f.GetNewContent(), Hunks: f.GetHunks(),
			ContentTruncated: f.GetContentTruncated(), HunksTruncated: f.GetHunksTruncated(),
		}) {
			return nil
		}
	}
}

// GitLog implements DirOps.
func (r Remote) GitLog(ctx context.Context, runDir string, limit int) (GitLogResult, error) {
	resp, err := r.Client.GitLog(ctx, &runnerpb.GitLogRequest{
		SessionId: r.ChatID, Limit: int32(limit), RunDir: RunDirRef(r.ChatID, runDir),
	})
	if err != nil {
		return GitLogResult{}, ToError(err)
	}
	res := GitLogResult{
		Commits: []GitCommit{}, CurrentBranch: resp.GetCurrentBranch(), TrackingBranch: resp.GetTrackingBranch(),
		Ahead: int(resp.GetAhead()), Behind: int(resp.GetBehind()), UnstashedCount: int(resp.GetUnstashedCount()),
	}
	for _, c := range resp.GetCommits() {
		res.Commits = append(res.Commits, GitCommit{
			Hash: c.GetHash(), ShortHash: c.GetShortHash(), Author: c.GetAuthor(), AuthorEmail: c.GetAuthorEmail(),
			Date: c.GetDate(), RelativeDate: c.GetRelativeDate(), Refs: c.GetRefs(), Message: c.GetMessage(),
		})
	}
	return res, nil
}

func (r Remote) gitOp(ctx context.Context, runDir, requestID string, call func(context.Context, *runnerpb.GitSessionRequest, ...grpc.CallOption) (*runnerpb.GitOpResponse, error)) (GitOpResult, error) {
	resp, err := call(ctx, &runnerpb.GitSessionRequest{
		SessionId: r.ChatID, RunDir: RunDirRef(r.ChatID, runDir), RequestId: requestID,
	})
	if err != nil {
		return GitOpResult{}, ToError(err)
	}
	return GitOpResult{Success: resp.GetSuccess(), Output: resp.GetOutput(), Error: resp.GetError()}, nil
}

// GitPush implements DirOps.
func (r Remote) GitPush(ctx context.Context, runDir, requestID string) (GitOpResult, error) {
	return r.gitOp(ctx, runDir, requestID, r.Client.GitPush)
}

// GitPull implements DirOps.
func (r Remote) GitPull(ctx context.Context, runDir, requestID string) (GitOpResult, error) {
	return r.gitOp(ctx, runDir, requestID, r.Client.GitPull)
}
