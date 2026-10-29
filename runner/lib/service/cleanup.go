package service

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// maxCleanupError caps one failure's error text (CleanupFailure.error_truncated).
const maxCleanupError = 64 * 1024

// maxCleanupIDs bounds the chat id lists of one call (a hostile or buggy
// backend cannot make the runner build unbounded sets).
const maxCleanupIDs = 1_000_000

// Cleanup implements RunnerService: it removes per-chat dirs and sweeps
// orphans, only beneath the runner's own data dirs. Chat ids are validated
// (runnerfs rejects anything but a plain chat id) and removal never follows a
// symlink. The result is cached per request_id so a retry is idempotent.
func (s *Service) Cleanup(_ context.Context, req *runnerpb.CleanupRequest) (*runnerpb.CleanupResponse, error) {
	if len(req.GetChatIds()) > maxCleanupIDs || len(req.GetKeepChatIds()) > maxCleanupIDs || len(req.GetOrphanTmpSweeps()) > maxCleanupIDs {
		return nil, status.Error(codes.InvalidArgument, "too many ids")
	}
	cutoff := req.GetCutoffUnixNano()
	if cutoff < 0 {
		return nil, status.Error(codes.InvalidArgument, "negative cutoff")
	}
	spec := runnerfs.CleanupSpec{ChatIDs: req.GetChatIds(), Keep: req.GetKeepChatIds()}
	if cutoff > 0 {
		spec.Cutoff = time.Unix(0, cutoff)
	}
	for _, sw := range req.GetOrphanTmpSweeps() {
		scope := runnerfs.ScopeFromProto(sw.GetDir().GetScope())
		if scope != "tmp" && scope != "session" {
			return nil, status.Error(codes.InvalidArgument, "orphan tmp sweep dir must be a tmp or session ref")
		}
		spec.TmpSweeps = append(spec.TmpSweeps, runnerfs.CleanupTmpSweep{
			ChatID: sw.GetChatId(), Scope: scope, Rel: sw.GetDir().GetRelOrPath(),
		})
	}
	v, err := s.idem.do("Cleanup", req.GetRequestId(), func() (any, error) {
		res := runnerfs.DefaultCleanupRoots().Cleanup(spec)
		resp := &runnerpb.CleanupResponse{Removed: int32(res.Removed)}
		for _, f := range res.Failures {
			msg, cut := trimText(strings.ToValidUTF8(f.Err, "?"), maxCleanupError)
			resp.Failures = append(resp.Failures, &runnerpb.CleanupFailure{ChatId: f.ChatID, Error: msg, ErrorTruncated: cut})
		}
		return resp, nil
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return v.(*runnerpb.CleanupResponse), nil
}

// QueryQuota implements RunnerService: the runner's full quota (limits,
// balance, refresh dates), optionally narrowed to the requested targets.
func (s *Service) QueryQuota(ctx context.Context, req *runnerpb.QueryQuotaRequest) (*runnerpb.QueryQuotaResponse, error) {
	query := s.quota
	if query == nil {
		query = agentwrapper.GetQuota
	}
	res, err := query(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	usages := runnerfs.QuotaToProto(res)
	if targets := req.GetTargets(); len(targets) > 0 {
		var kept []*runnerpb.ModelUsage
		for _, u := range usages {
			for _, t := range targets {
				if t.GetCli() == u.GetCli() && (t.GetModel() == "" || t.GetModel() == u.GetModel()) {
					kept = append(kept, u)
					break
				}
			}
		}
		usages = kept
	}
	return &runnerpb.QueryQuotaResponse{Usages: usages}, nil
}
