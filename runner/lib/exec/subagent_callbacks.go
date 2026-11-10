package exec

import (
	"context"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// registerSubagentRoutes adds the subagent CLI routes. The caller's chat and
// agent are never read from the request: the backend takes them from the run.
func (s *statusServer) registerSubagentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+sandboxsock.PathSubagentSpawn, s.handleSubagentSpawn)
	mux.HandleFunc("GET "+sandboxsock.PathSubagentList, s.handleSubagentList)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentStatus, s.handleSubagentStatus)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentKill, s.handleSubagentKill)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentSend, s.handleSubagentSend)
}

// failSubagent reports a failed subagent call with an HTTP status that
// follows the backend's gRPC code, so the CLI can say what went wrong.
func (s *statusServer) failSubagent(w http.ResponseWriter, ctx context.Context, op string, err error) {
	if ctx.Err() != nil {
		s.failCall(w, ctx, op, err)
		return
	}
	code := http.StatusBadGateway
	switch status.Code(err) {
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.PermissionDenied:
		code = http.StatusForbidden
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.ResourceExhausted:
		code = http.StatusTooManyRequests
	case codes.FailedPrecondition:
		code = http.StatusServiceUnavailable
	}
	if code == http.StatusBadGateway {
		s.failCall(w, ctx, op, err)
		return
	}
	msg, _ := truncateError(status.Convert(err).Message())
	writeJSON(w, code, sandboxsock.ErrorResponse{Error: msg})
}

func subagentInfoJSON(i *runnerpb.SubagentInfo) sandboxsock.SubagentInfo {
	return sandboxsock.SubagentInfo{
		ID:             i.GetId(),
		Label:          i.GetLabel(),
		Agent:          i.GetAgent(),
		Model:          i.GetModel(),
		Readonly:       i.GetReadonly(),
		State:          i.GetState(),
		Summary:        i.GetSummary(),
		LastActivityMs: i.GetLastActivityUnixMs(),
	}
}

func (s *statusServer) handleSubagentSpawn(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentSpawnRequest
	if !decodeCall(w, r, &req) {
		return
	}
	agentID, prompt := clean(req.AgentID), clean(req.Prompt)
	if agentID == "" || strings.TrimSpace(prompt) == "" {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "agent_id and prompt are required"})
		return
	}
	if len(prompt) > maxCallText {
		http.Error(w, "prompt too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.SpawnSubagentResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{
			Meta:     s.meta(seq),
			AgentId:  agentID,
			Model:    clean(req.Model),
			Readonly: req.Readonly,
			Label:    clean(req.Label),
			Prompt:   prompt,
		})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-spawn", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentSpawnResponse{ID: resp.GetId()})
}

func (s *statusServer) handleSubagentList(w http.ResponseWriter, r *http.Request) {
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.ListSubagentsResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.ListSubagents(ctx, &runnerpb.ListSubagentsRequest{Meta: s.meta(seq)})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-list", err)
		return
	}
	out := sandboxsock.SubagentListResponse{Subagents: []sandboxsock.SubagentInfo{}}
	for _, i := range resp.GetSubagents() {
		out.Subagents = append(out.Subagents, subagentInfoJSON(i))
	}
	writeJSON(w, http.StatusOK, out)
}

// subagentID decodes and checks the body of the id-taking routes.
func subagentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req sandboxsock.SubagentIDRequest
	if !decodeCall(w, r, &req) {
		return "", false
	}
	id := clean(req.ID)
	if id == "" || len(id) > maxMessageID {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a valid subagent id is required"})
		return "", false
	}
	return id, true
}

func (s *statusServer) handleSubagentStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := subagentID(w, r)
	if !ok {
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.SubagentStatusResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.SubagentStatus(ctx, &runnerpb.SubagentStatusRequest{Meta: s.meta(seq), Id: id})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-status", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentStatusResponse{Subagent: subagentInfoJSON(resp.GetSubagent())})
}

func (s *statusServer) handleSubagentKill(w http.ResponseWriter, r *http.Request) {
	id, ok := subagentID(w, r)
	if !ok {
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.KillSubagentResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.KillSubagent(ctx, &runnerpb.KillSubagentRequest{Meta: s.meta(seq), Id: id})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-kill", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentKillResponse{Stopped: int(resp.GetStopped())})
}

func (s *statusServer) handleSubagentSend(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentSendRequest
	if !decodeCall(w, r, &req) {
		return
	}
	id, text := clean(req.ID), clean(req.Text)
	if id == "" || len(id) > maxMessageID || strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a valid subagent id and a message are required"})
		return
	}
	if len(text) > maxCallText {
		http.Error(w, "message too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.SendToSubagentResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.SendToSubagent(ctx, &runnerpb.SendToSubagentRequest{Meta: s.meta(seq), Id: id, Text: text})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-send", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentSendResponse{Delivery: resp.GetDelivery()})
}
