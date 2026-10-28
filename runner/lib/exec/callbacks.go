package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/metrics"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
)

const (
	// maxCallText caps the sandbox-supplied question/prompt of a callback
	// (the protocol's 4 MiB cap for request text); larger ones are rejected.
	maxCallText = 4 << 20
	// maxMetricsEvents bounds one RecordMetrics call.
	maxMetricsEvents = 1000
	// maxMessageID bounds a sandbox-supplied ask-user message id.
	maxMessageID = 128

	callRetryMin = 100 * time.Millisecond
	callRetryMax = 5 * time.Second
)

// validMessageID restricts sandbox-supplied ask-user message ids.
var validMessageID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// CallbacksFunc returns the AgentCallbackService client of the current backend
// connection, or conn.ErrNotConnected while there is none.
type CallbacksFunc func() (runnerpb.AgentCallbackServiceClient, error)

// registerCallbacks adds the sandbox -> backend routes to mux.
func (s *statusServer) registerCallbacks(mux *http.ServeMux) {
	mux.HandleFunc("POST "+sandboxsock.PathAskUser, s.handleAskUser)
	mux.HandleFunc("POST "+sandboxsock.PathCallPeer, s.handleCallPeer)
	mux.HandleFunc("GET "+sandboxsock.PathFindPeer, s.handleFindPeer)
	mux.HandleFunc("POST "+sandboxsock.PathMetrics, s.handleMetrics)
}

// nextCallSeq allocates the call_seq of a new sandbox call; it increases per
// run. The seq is chosen once per sandbox request and reused by every retry
// of that call.
func (s *statusServer) nextCallSeq() uint64 {
	return s.callSeq.Add(1)
}

// meta stamps the run identity of a call. Whatever identity the sandbox put in
// its request is never consulted.
func (s *statusServer) meta(seq uint64) *runnerpb.CallMeta {
	return &runnerpb.CallMeta{RunId: s.sr.GetRunId(), CallSeq: seq}
}

// callBackend runs fn against the backend callback client until it succeeds,
// fails permanently or ctx ends. A dropped connection (or none yet) is retried
// with the same call, i.e. the same call_seq, so the backend attaches to the
// in-flight call or answers from its cache. There is deliberately no timeout:
// blocking calls such as ask-user wait for as long as it takes.
func (s *statusServer) callBackend(ctx context.Context, fn func(context.Context, runnerpb.AgentCallbackServiceClient) error) error {
	if s.callbacks == nil {
		return errors.New("backend callbacks are not available on this runner")
	}
	delay := s.retryMin
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cb, err := s.callbacks()
		if err == nil {
			err = fn(ctx, cb)
			if err == nil {
				return nil
			}
			if !retryable(ctx, err) {
				return err
			}
		} else if !errors.Is(err, conn.ErrNotConnected) {
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay = min(delay*2, s.retryMax)
	}
}

// retryable reports whether err is a transport failure of the backend
// connection (not an answer of the backend, and not our own cancellation).
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.Canceled, codes.DeadlineExceeded, codes.Aborted:
		return true
	}
	return false
}

// callContext unblocks when either the sandbox request or the run goes away.
func (s *statusServer) callContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func decodeCall(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCallText+(1<<20))).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// failCall reports a failed forward to the sandbox. A cancelled call (the
// sandbox went away or the run ended) has nobody left to answer.
func (s *statusServer) failCall(w http.ResponseWriter, ctx context.Context, op string, err error) {
	log.Warn().Err(err).Str("run_id", s.sr.GetRunId()).Str("op", op).Msg("failed to forward sandbox call to backend")
	if ctx.Err() != nil {
		http.Error(w, "cancelled", http.StatusServiceUnavailable)
		return
	}
	msg, _ := truncateError(status.Convert(err).Message())
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
}

func clean(s string) string { return strings.ToValidUTF8(s, "�") }

func (s *statusServer) agentName() string {
	if s.agent == nil {
		return ""
	}
	return s.agent.Config.Name
}

func (s *statusServer) handleAskUser(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.AskUserRequest
	if !decodeCall(w, r, &req) {
		return
	}
	question := clean(req.Question)
	if question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}
	if len(question) > maxCallText {
		http.Error(w, "question too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	// The runner always supplies the message id: the sandbox's if it is
	// well-formed, else one derived from run_id and call_seq (stable across
	// retries).
	msgID := req.MessageID
	if msgID == "" || len(msgID) > maxMessageID || !validMessageID.MatchString(msgID) {
		msgID = fmt.Sprintf("ask-%s-%d", s.sr.GetRunId(), seq)
	}
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.AskUserResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.AskUser(ctx, &runnerpb.AskUserRequest{
			Meta:      s.meta(seq),
			ChatId:    s.sr.GetChatId(),
			AgentName: s.agentName(),
			Question:  question,
			MessageId: msgID,
			Model:     clean(req.Model),
		})
		return err
	})
	if err != nil {
		s.failCall(w, ctx, "ask-user", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.AskUserResponse{Reply: resp.GetReply(), Error: resp.GetError()})
}

func (s *statusServer) handleCallPeer(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.CallPeerRequest
	if !decodeCall(w, r, &req) {
		return
	}
	agentID, prompt := clean(req.AgentID), clean(req.Prompt)
	if agentID == "" || strings.TrimSpace(prompt) == "" {
		http.Error(w, "agent_id and prompt are required", http.StatusBadRequest)
		return
	}
	if len(prompt) > maxCallText {
		http.Error(w, "prompt too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	meta, _ := json.Marshal(map[string]any{
		"internal":          true,
		"source":            "call-peer",
		"caller_agent_id":   s.sr.GetAgentId(),
		"caller_agent_name": s.agentName(),
	})
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.CallPeerResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.CallPeer(ctx, &runnerpb.CallPeerRequest{
			Meta:         s.meta(seq),
			AgentId:      agentID,
			Prompt:       prompt,
			ChatId:       s.sr.GetChatId(),
			MetadataJson: meta,
		})
		return err
	})
	if err != nil {
		s.failCall(w, ctx, "call-peer", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.CallPeerResponse{Status: resp.GetStatus(), Output: resp.GetOutput(), Error: resp.GetError()})
}

func (s *statusServer) handleFindPeer(w http.ResponseWriter, r *http.Request) {
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.FindPeerResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.FindPeer(ctx, &runnerpb.FindPeerRequest{Meta: s.meta(seq), ChatId: s.sr.GetChatId()})
		return err
	})
	if err != nil {
		s.failCall(w, ctx, "find-peer", err)
		return
	}
	peers := resp.GetPeers()
	if peers == nil {
		peers = []string{}
	}
	writeJSON(w, http.StatusOK, peers)
}

func (s *statusServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var events []metrics.Event
	if !decodeCall(w, r, &events) {
		return
	}
	if len(events) > maxMetricsEvents {
		events = events[:maxMetricsEvents]
	}
	pb := make([]*runnerpb.MetricsEvent, 0, len(events))
	for _, ev := range events {
		pb = append(pb, &runnerpb.MetricsEvent{Kind: clean(string(ev.Kind)), Tokens: ev.Tokens, Bytes: ev.Bytes})
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) error {
		_, err := cb.RecordMetrics(ctx, &runnerpb.RecordMetricsRequest{Meta: s.meta(seq), Events: pb})
		return err
	})
	if err != nil {
		s.failCall(w, ctx, "metrics", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
