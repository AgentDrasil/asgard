package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/accounts"
	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/llms"
	"github.com/AgentDrasil/asgard/pkg/bwrap"
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

	var wantedCLIs map[string]bool
	if targets := req.GetTargets(); len(targets) > 0 {
		wantedCLIs = make(map[string]bool, len(targets))
		for _, t := range targets {
			wantedCLIs[t.GetCli()] = true
		}
	}

	var (
		res       map[string][]llms.ModelUsage
		queryErr  error
		accQuotas map[string]*accountQuotaBatch
		wg        sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		res, queryErr = query(ctx)
	}()
	go func() {
		defer wg.Done()
		accQuotas = s.fetchAccountQuotas(ctx, wantedCLIs)
	}()
	wg.Wait()

	if queryErr != nil {
		return nil, status.Error(codes.Internal, queryErr.Error())
	}
	if res != nil {
		mergeAccountQuotas(res, accQuotas)
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

// accountQuotaCLIs are the CLIs whose quota is reported per runner account.
var accountQuotaCLIs = []string{"agy", "claude"}

// accountQuotaTimeout bounds the whole per-account quota fan-out.
const accountQuotaTimeout = 40 * time.Second

type accountQuotaBatch struct {
	cli        string
	perAccount [][]llms.ModelUsage
	answered   []bool
	failed     []*llms.ModelUsage
}

// fetchAccountQuotas concurrently queries all configured accounts across all
// account-aware CLIs (optionally filtered by wantedCLIs).
func (s *Service) fetchAccountQuotas(ctx context.Context, wantedCLIs map[string]bool) map[string]*accountQuotaBatch {
	fetch := s.accountQuota
	if fetch == nil {
		fetch = bwrap.QueryAccountQuota
	}
	// `aw usage --cli` skips the allow-list (CheckTarget needs every model),
	// so the per-account usages are filtered here like the plain query is.
	cfg, err := awconfig.LoadConfig()
	if err != nil {
		log.Warn().Err(err).Msg("loading aw config for quota filtering failed")
	}
	ctx, cancel := context.WithTimeout(ctx, accountQuotaTimeout)
	defer cancel()

	batches := make(map[string]*accountQuotaBatch)
	var wg sync.WaitGroup
	for _, cli := range accountQuotaCLIs {
		if wantedCLIs != nil && !wantedCLIs[cli] {
			continue
		}
		aliases, err := accounts.List(cli)
		if err != nil {
			log.Warn().Err(err).Str("cli", cli).Msg("listing accounts for quota failed")
			continue
		}
		if len(aliases) == 0 {
			continue
		}
		batch := &accountQuotaBatch{
			cli:        cli,
			perAccount: make([][]llms.ModelUsage, len(aliases)),
			answered:   make([]bool, len(aliases)),
			failed:     make([]*llms.ModelUsage, len(aliases)),
		}
		batches[cli] = batch
		for i, alias := range aliases {
			wg.Add(1)
			go func(b *accountQuotaBatch, idx int, c, a string) {
				defer wg.Done()
				usages, err := fetch(ctx, c, a)
				if err != nil {
					log.Warn().Err(err).Str("cli", c).Str("account", a).Msg("account quota query failed")
					st := llms.StatusUsage(err)
					st.Account = a
					b.failed[idx] = &st
					return
				}
				usages = agentwrapper.FilterAllowedUsages(cfg, c, usages)
				for j := range usages {
					usages[j].Account = a
				}
				b.perAccount[idx] = usages
				b.answered[idx] = true
			}(batch, i, cli, alias)
		}
	}
	wg.Wait()
	return batches
}

// mergeAccountQuotas replaces res[cli] of each account-aware CLI that has
// accounts with one usage set per account (Account = alias, alias order).
// A failing account contributes a status-only entry; when no account answers,
// the legacy models of the CLI are kept next to those status entries.
func mergeAccountQuotas(res map[string][]llms.ModelUsage, batches map[string]*accountQuotaBatch) {
	for cli, batch := range batches {
		var merged []llms.ModelUsage
		answered := false
		for i, u := range batch.perAccount {
			answered = answered || batch.answered[i]
			merged = append(merged, u...)
			if batch.failed[i] != nil {
				merged = append(merged, *batch.failed[i])
			}
		}
		if !answered {
			var legacy []llms.ModelUsage
			for _, u := range res[cli] {
				if !u.IsStatusOnly() {
					legacy = append(legacy, u)
				}
			}
			res[cli] = append(legacy, merged...)
			continue
		}
		res[cli] = merged
	}
}
