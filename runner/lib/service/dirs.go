package service

import (
	"context"
	"unicode/utf8"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// noChatFS is the Local of requests that name no chat (Subdirs): only the
// agents' allowed dirs are reachable.
func (s *Service) noChatFS() runnerfs.Local {
	l := runnerfs.NewConfined("", s.allowedRunDirs, s.readonlyDirs)
	l.GitExec = s.gitExec
	return l
}

// Tree implements RunnerService.
func (s *Service) Tree(ctx context.Context, req *runnerpb.TreeRequest) (*runnerpb.TreeResponse, error) {
	rd, err := runDirString(req.GetSessionId(), req.GetRunDir())
	if err != nil {
		return nil, grpcErr(err)
	}
	res, err := s.fsFor(req.GetSessionId()).Tree(ctx, runnerfs.TreeRequest{
		RunDir: rd, Path: req.GetPath(), Scope: runnerfs.ScopeFromProto(req.GetScope()),
		Limit: int(req.GetLimit()), PageToken: req.GetPageToken(),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &runnerpb.TreeResponse{Path: res.Path, NextPageToken: res.NextPageToken}
	for _, e := range res.Entries {
		resp.Entries = append(resp.Entries, &runnerpb.FileTreeEntry{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, Ext: e.Ext})
	}
	return resp, nil
}

// Search implements RunnerService.
func (s *Service) Search(ctx context.Context, req *runnerpb.SearchRequest) (*runnerpb.SearchResponse, error) {
	rd, err := runDirString(req.GetSessionId(), req.GetRunDir())
	if err != nil {
		return nil, grpcErr(err)
	}
	hits, err := s.fsFor(req.GetSessionId()).Search(ctx, runnerfs.SearchRequest{RunDir: rd, Query: req.GetQuery(), Limit: int(req.GetLimit())})
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &runnerpb.SearchResponse{}
	for _, h := range hits {
		resp.Files = append(resp.Files, &runnerpb.FileSearchResult{
			Path: h.Path, Name: h.Name, Ext: h.Ext, Size: h.Size, Scope: runnerfs.ScopeToProto(h.Scope),
		})
	}
	return resp, nil
}

// Subdirs implements RunnerService.
func (s *Service) Subdirs(ctx context.Context, req *runnerpb.SubdirsRequest) (*runnerpb.SubdirsResponse, error) {
	res, err := s.noChatFS().Subdirs(ctx, runnerfs.SubdirsRequest{Dir: req.GetDir(), Limit: int(req.GetLimit()), PageToken: req.GetPageToken()})
	if err != nil {
		return nil, grpcErr(err)
	}
	return &runnerpb.SubdirsResponse{Subdirs: res.Subdirs, GitRoot: res.GitRoot, NextPageToken: res.NextPageToken}, nil
}

// GitDiff implements RunnerService: one frame per changed file.
func (s *Service) GitDiff(req *runnerpb.GitDiffRequest, stream runnerpb.RunnerService_GitDiffServer) error {
	rd, err := runDirString(req.GetSessionId(), req.GetRunDir())
	if err != nil {
		return grpcErr(err)
	}
	var sendErr error
	err = s.fsFor(req.GetSessionId()).GitDiff(stream.Context(), runnerfs.GitDiffRequest{RunDir: rd, Commit: req.GetCommit()}, func(f runnerfs.GitDiffFile) bool {
		sendErr = stream.Send(&runnerpb.GitDiffFile{
			OldPath: f.OldPath, NewPath: f.NewPath, Status: f.Status, OldContent: f.OldContent, NewContent: f.NewContent,
			Hunks: f.Hunks, ContentTruncated: f.ContentTruncated, HunksTruncated: f.HunksTruncated,
		})
		return sendErr == nil
	})
	if sendErr != nil {
		return sendErr
	}
	return grpcErr(err)
}

// GitLog implements RunnerService.
func (s *Service) GitLog(ctx context.Context, req *runnerpb.GitLogRequest) (*runnerpb.GitLogResponse, error) {
	rd, err := runDirString(req.GetSessionId(), req.GetRunDir())
	if err != nil {
		return nil, grpcErr(err)
	}
	res, err := s.fsFor(req.GetSessionId()).GitLog(ctx, rd, int(req.GetLimit()))
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &runnerpb.GitLogResponse{
		CurrentBranch: res.CurrentBranch, TrackingBranch: res.TrackingBranch,
		Ahead: int32(res.Ahead), Behind: int32(res.Behind), UnstashedCount: int32(res.UnstashedCount),
	}
	for _, c := range res.Commits {
		resp.Commits = append(resp.Commits, &runnerpb.GitCommit{
			Hash: c.Hash, ShortHash: c.ShortHash, Author: c.Author, AuthorEmail: c.AuthorEmail,
			Date: c.Date, RelativeDate: c.RelativeDate, Refs: c.Refs, Message: c.Message,
		})
	}
	return resp, nil
}

// trimText cuts s to max bytes on a rune boundary.
func trimText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

func (s *Service) gitOp(ctx context.Context, method string, req *runnerpb.GitSessionRequest, do func(runnerfs.Local, string) (runnerfs.GitOpResult, error)) (*runnerpb.GitOpResponse, error) {
	rd, err := runDirString(req.GetSessionId(), req.GetRunDir())
	if err != nil {
		return nil, grpcErr(err)
	}
	v, err := s.idem.do(method, req.GetRequestId(), func() (any, error) {
		res, err := do(s.fsFor(req.GetSessionId()), rd)
		if err != nil {
			return nil, grpcErr(err)
		}
		out, outCut := trimText(res.Output, runnerfs.MaxGitError)
		msg, msgCut := trimText(res.Error, runnerfs.MaxGitError)
		return &runnerpb.GitOpResponse{Success: res.Success, Output: out, Error: msg, ErrorTruncated: outCut || msgCut}, nil
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return v.(*runnerpb.GitOpResponse), nil
}

// GitPush implements RunnerService.
func (s *Service) GitPush(ctx context.Context, req *runnerpb.GitSessionRequest) (*runnerpb.GitOpResponse, error) {
	return s.gitOp(ctx, "GitPush", req, func(l runnerfs.Local, rd string) (runnerfs.GitOpResult, error) {
		return l.GitPush(ctx, rd, req.GetRequestId())
	})
}

// GitPull implements RunnerService.
func (s *Service) GitPull(ctx context.Context, req *runnerpb.GitSessionRequest) (*runnerpb.GitOpResponse, error) {
	return s.gitOp(ctx, "GitPull", req, func(l runnerfs.Local, rd string) (runnerfs.GitOpResult, error) {
		return l.GitPull(ctx, rd, req.GetRequestId())
	})
}
