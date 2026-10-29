package runnerfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// CleanupRequest is a backend cleanup call to one runner.
type CleanupRequest struct {
	// ChatIDs are removed (tmp, session and CA bundle dirs).
	ChatIDs []string
	// Cutoff > zero enables the orphan sweep: dirs not in Keep whose latest
	// mtime is older than Cutoff are removed.
	Cutoff time.Time
	Keep   []string
	// TmpSweeps remove dangling atomic-write temp files.
	TmpSweeps []TmpSweepRef
	// RequestID makes a retry of the same call idempotent (random if empty).
	RequestID string
}

// TmpSweepRef selects the session-scoped dir ("workflows" below the chat's
// session dir, for instance) swept for orphan temp files.
type TmpSweepRef struct {
	ChatID string
	// Scope is "tmp" or "session".
	Scope string
	Rel   string
}

// CleanupOutcome is what the runner reported.
type CleanupOutcome struct {
	Removed int
	// Failures maps a chat id ("" for the orphan sweep) to its errors.
	Failures map[string][]string
}

// Cleanup calls the runner's Cleanup RPC.
func Cleanup(ctx context.Context, client runnerpb.RunnerServiceClient, req CleanupRequest) (CleanupOutcome, error) {
	rid := req.RequestID
	if rid == "" {
		rid = newRequestID()
	}
	pb := &runnerpb.CleanupRequest{ChatIds: req.ChatIDs, KeepChatIds: req.Keep, RequestId: rid}
	if !req.Cutoff.IsZero() {
		pb.CutoffUnixNano = req.Cutoff.UnixNano()
	}
	for _, s := range req.TmpSweeps {
		pb.OrphanTmpSweeps = append(pb.OrphanTmpSweeps, &runnerpb.OrphanTmpSweep{
			ChatId: s.ChatID, Dir: &runnerpb.DirRef{Scope: ScopeToProto(s.Scope), RelOrPath: s.Rel},
		})
	}
	resp, err := client.Cleanup(ctx, pb)
	if err != nil {
		return CleanupOutcome{}, ToError(err)
	}
	out := CleanupOutcome{Removed: int(resp.GetRemoved()), Failures: map[string][]string{}}
	for _, f := range resp.GetFailures() {
		out.Failures[f.GetChatId()] = append(out.Failures[f.GetChatId()], f.GetError())
	}
	return out, nil
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
