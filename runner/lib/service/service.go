// Package service implements the runner-hosted RunnerService: the backend's
// access to a chat's directories on this runner. File logic lives in
// backend/lib/runnerfs (Local); this package adds the gRPC framing, error
// mapping and request_id idempotency.
package service

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

const readChunk = 256 * 1024

// Options configures a Service.
type Options struct {
	// IdemTTL and IdemMax bound the request_id result cache.
	IdemTTL time.Duration
	IdemMax int
	// Agents lists the runner's effective agents. A chat's workspace (run dir)
	// must lie in one of their run_dirs or writable mount_dirs, or in the
	// chat's own tmp/session dirs; nothing else is reachable.
	Agents func() ([]*agentspec.Agent, error)
	// Shell is the terminal shell (empty: fish, bash or sh from PATH).
	Shell string
	// GitExec runs git invocations (the runner passes the sandboxed one); nil
	// runs git directly (tests, development).
	GitExec runnerfs.GitExecFunc
}

// Service implements runnerpb.RunnerServiceServer. RPCs that are not
// implemented yet answer Unimplemented.
type Service struct {
	runnerpb.UnimplementedRunnerServiceServer
	idem    *idemCache
	agents  func() ([]*agentspec.Agent, error)
	shell   string
	gitExec runnerfs.GitExecFunc
	ttyd    ttydSessions
}

// New returns a Service.
func New(opts Options) *Service {
	return &Service{idem: newIdemCache(opts.IdemTTL, opts.IdemMax), agents: opts.Agents, shell: opts.Shell, gitExec: opts.GitExec}
}

// grpcErr maps a runnerfs error onto a gRPC status.
func grpcErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	c := codes.Internal
	switch runnerfs.CodeOf(err) {
	case runnerfs.CodeNotFound:
		c = codes.NotFound
	case runnerfs.CodeDenied:
		c = codes.PermissionDenied
	case runnerfs.CodeBadRequest:
		c = codes.InvalidArgument
	case runnerfs.CodeTooLarge:
		c = codes.ResourceExhausted
	case runnerfs.CodeIsDir:
		c = codes.FailedPrecondition
	case runnerfs.CodeUnavailable:
		c = codes.Unavailable
	}
	return status.Error(c, err.Error())
}

// allowedRunDirs is the union of the agents' and workflows' run_dirs and mount
// dirs (readonly mounts only when write is false).
func (s *Service) allowedRunDirs(write bool) []string {
	if s.agents == nil {
		return nil
	}
	list, err := s.agents()
	if err != nil {
		return nil
	}
	return agents.AllowedDirs(list, write)
}

func (s *Service) readonlyDirs() []string {
	if s.agents == nil {
		return nil
	}
	list, err := s.agents()
	if err != nil {
		return nil
	}
	return agents.ReadonlyDirs(list)
}

func (s *Service) fsFor(chatID string) runnerfs.Local {
	l := runnerfs.NewConfined(chatID, s.allowedRunDirs, s.readonlyDirs)
	l.GitExec = s.gitExec
	return l
}

// runDirString turns a DirRef into the run dir string Local interprets.
// TMP/SESSION refs resolve inside the chat's dirs; ABSOLUTE carries the
// stored session run dir, which Local normalizes (tmp/session forms).
func runDirString(chatID string, ref *runnerpb.DirRef) (string, error) {
	if ref == nil {
		return "", nil
	}
	switch ref.GetScope() {
	case runnerpb.FileScope_FILE_SCOPE_TMP, runnerpb.FileScope_FILE_SCOPE_SESSION:
		if !agents.IsValidChatID(chatID) {
			return "", runnerfs.Errf(runnerfs.CodeBadRequest, "invalid session id")
		}
		ns := runnerfs.ScopeFromProto(ref.GetScope())
		rel := ref.GetRelOrPath()
		if rel != "" && !filepath.IsLocal(rel) {
			return "", runnerfs.Errf(runnerfs.CodeDenied, "access denied: dir escapes the %s dir", ns)
		}
		return filepath.Join(agents.GetSessionScopedBaseDir(ns, chatID), rel), nil
	case runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, runnerpb.FileScope_FILE_SCOPE_UNSPECIFIED:
		return ref.GetRelOrPath(), nil
	}
	return "", runnerfs.Errf(runnerfs.CodeBadRequest, "unsupported dir scope %s", ref.GetScope())
}

func pathRefOf(ref *runnerpb.PathRef) (runnerfs.PathRef, error) {
	rd, err := runDirString(ref.GetSessionId(), ref.GetRunDir())
	if err != nil {
		return runnerfs.PathRef{}, err
	}
	return runnerfs.PathRef{RunDir: rd, Path: ref.GetPath(), Scope: runnerfs.ScopeFromProto(ref.GetScope())}, nil
}

// sendFile streams a file as ReadFileChunks: header first, then the byte range
// [offset, offset+length) (length 0: to the end).
func sendFile(f *runnerfs.File, offset, length int64, send func(*runnerpb.ReadFileChunk) error) error {
	defer func() { _ = f.Close() }()
	if offset < 0 || length < 0 || offset > f.Size {
		return status.Error(codes.OutOfRange, "invalid range")
	}
	if err := send(&runnerpb.ReadFileChunk{Payload: &runnerpb.ReadFileChunk_Info{Info: &runnerpb.FileInfo{
		Path: f.Path, Name: f.Name, Ext: f.Ext, Size: f.Size, IsBinary: f.IsBinary,
		UpdatedAtUnixNano: f.ModTime.UnixNano(), Mime: f.Mime,
	}}}); err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	var src io.Reader = f
	if length > 0 {
		src = io.LimitReader(f, length)
	}
	buf := make([]byte, readChunk)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if serr := send(&runnerpb.ReadFileChunk{Payload: &runnerpb.ReadFileChunk_Data{Data: buf[:n:n]}}); serr != nil {
				return serr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
}

// ReadFile implements RunnerService.
func (s *Service) ReadFile(req *runnerpb.ReadFileRequest, stream runnerpb.RunnerService_ReadFileServer) error {
	rd, err := runDirString(req.GetSessionId(), req.GetRunDir())
	if err != nil {
		return grpcErr(err)
	}
	f, err := s.fsFor(req.GetSessionId()).Open(stream.Context(), runnerfs.ReadRequest{
		RunDir: rd, Path: req.GetPath(), Scope: runnerfs.ScopeFromProto(req.GetScope()), Raw: req.GetRaw(),
	})
	if err != nil {
		return grpcErr(err)
	}
	return sendFile(f, req.GetOffset(), req.GetLength(), stream.Send)
}

// GetAttachment implements RunnerService.
func (s *Service) GetAttachment(req *runnerpb.GetAttachmentRequest, stream runnerpb.RunnerService_GetAttachmentServer) error {
	f, err := s.fsFor(req.GetSessionId()).OpenAttachment(stream.Context(), req.GetFilename())
	if err != nil {
		return grpcErr(err)
	}
	return sendFile(f, req.GetOffset(), req.GetLength(), stream.Send)
}

// chunkReader presents the data frames of a client stream as an io.Reader.
type chunkReader struct {
	recv func() ([]byte, error)
	buf  []byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		b, err := r.recv()
		if err != nil {
			return 0, err
		}
		r.buf = b
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// PutAttachment implements RunnerService.
func (s *Service) PutAttachment(stream runnerpb.RunnerService_PutAttachmentServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if h == nil {
		return status.Error(codes.InvalidArgument, "first message must be the header")
	}
	v, err := s.idem.do("PutAttachment", h.GetRequestId(), func() (any, error) {
		r := &chunkReader{recv: func() ([]byte, error) {
			m, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			return m.GetData(), nil
		}}
		res, err := s.fsFor(h.GetSessionId()).PutAttachment(stream.Context(), h.GetRequestId(), h.GetFilename(), r)
		if err != nil {
			return nil, grpcErr(err)
		}
		return &runnerpb.PutAttachmentResponse{Filename: res.Name, Size: res.Size}, nil
	})
	if err != nil {
		return grpcErr(err)
	}
	return stream.SendAndClose(v.(*runnerpb.PutAttachmentResponse))
}

// Stat implements RunnerService.
func (s *Service) Stat(ctx context.Context, req *runnerpb.StatRequest) (*runnerpb.StatResponse, error) {
	ref, err := pathRefOf(req.GetRef())
	if err != nil {
		return nil, grpcErr(err)
	}
	st, err := s.fsFor(req.GetRef().GetSessionId()).Stat(ctx, ref)
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &runnerpb.StatResponse{Exists: st.Exists, IsDir: st.IsDir, Size: st.Size}
	if st.Exists {
		resp.UpdatedAtUnixNano = st.ModTime.UnixNano()
	}
	return resp, nil
}

// WriteFile implements RunnerService.
func (s *Service) WriteFile(stream runnerpb.RunnerService_WriteFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if h == nil {
		return status.Error(codes.InvalidArgument, "first message must be the header")
	}
	ref, err := pathRefOf(h.GetRef())
	if err != nil {
		return grpcErr(err)
	}
	v, err := s.idem.do("WriteFile", h.GetRequestId(), func() (any, error) {
		r := &chunkReader{recv: func() ([]byte, error) {
			m, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			return m.GetData(), nil
		}}
		n, err := s.fsFor(h.GetRef().GetSessionId()).WriteFile(stream.Context(), runnerfs.WriteRequest{
			Ref: ref, Mkdir: h.GetMkdir(), Mode: h.GetMode(), Atomic: h.GetAtomic(), Sync: h.GetSync(),
			SkipIfUnchanged: h.GetSkipIfUnchanged(), RequestID: h.GetRequestId(),
		}, r)
		if err != nil {
			return nil, grpcErr(err)
		}
		return &runnerpb.WriteFileResponse{Size: n}, nil
	})
	if err != nil {
		return grpcErr(err)
	}
	return stream.SendAndClose(v.(*runnerpb.WriteFileResponse))
}

// AppendFile implements RunnerService.
func (s *Service) AppendFile(ctx context.Context, req *runnerpb.AppendFileRequest) (*runnerpb.AppendFileResponse, error) {
	ref, err := pathRefOf(req.GetRef())
	if err != nil {
		return nil, grpcErr(err)
	}
	v, err := s.idem.do("AppendFile", req.GetRequestId(), func() (any, error) {
		err := s.fsFor(req.GetRef().GetSessionId()).AppendFile(ctx, runnerfs.AppendRequest{
			Ref: ref, Mkdir: req.GetMkdir(), Data: req.GetData(), Sync: req.GetSync(), RequestID: req.GetRequestId(),
		})
		return &runnerpb.AppendFileResponse{}, grpcErr(err)
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return v.(*runnerpb.AppendFileResponse), nil
}

// Mkdir implements RunnerService.
func (s *Service) Mkdir(ctx context.Context, req *runnerpb.MkdirRequest) (*runnerpb.MkdirResponse, error) {
	ref, err := pathRefOf(req.GetRef())
	if err != nil {
		return nil, grpcErr(err)
	}
	v, err := s.idem.do("Mkdir", req.GetRequestId(), func() (any, error) {
		err := s.fsFor(req.GetRef().GetSessionId()).Mkdir(ctx, ref, req.GetRequestId())
		return &runnerpb.MkdirResponse{}, grpcErr(err)
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return v.(*runnerpb.MkdirResponse), nil
}

// Remove implements RunnerService.
func (s *Service) Remove(ctx context.Context, req *runnerpb.RemoveRequest) (*runnerpb.RemoveResponse, error) {
	ref, err := pathRefOf(req.GetRef())
	if err != nil {
		return nil, grpcErr(err)
	}
	v, err := s.idem.do("Remove", req.GetRequestId(), func() (any, error) {
		err := s.fsFor(req.GetRef().GetSessionId()).Remove(ctx, ref, req.GetRecursive(), req.GetRequestId())
		return &runnerpb.RemoveResponse{}, grpcErr(err)
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return v.(*runnerpb.RemoveResponse), nil
}
