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
	mux.HandleFunc("GET "+sandboxsock.PathSubagentTree, s.handleSubagentTree)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentStatus, s.handleSubagentStatus)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentKill, s.handleSubagentKill)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentSend, s.handleSubagentSend)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentWait, s.handleSubagentWait)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentReply, s.handleSubagentReply)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentAsk, s.handleSubagentAsk)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentAnswer, s.handleSubagentAnswer)
	mux.HandleFunc("GET "+sandboxsock.PathSubagentQuestions, s.handleSubagentQuestions)
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

func (s *statusServer) handleSubagentTree(w http.ResponseWriter, r *http.Request) {
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.SubagentTreeResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.SubagentTree(ctx, &runnerpb.SubagentTreeRequest{Meta: s.meta(seq)})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-tree", err)
		return
	}
	out := sandboxsock.SubagentTreeResponse{Nodes: []sandboxsock.SubagentTreeNode{}, Truncated: resp.GetTruncated()}
	for _, n := range resp.GetNodes() {
		i := n.GetInfo()
		out.Nodes = append(out.Nodes, sandboxsock.SubagentTreeNode{
			ID:       i.GetId(),
			ParentID: i.GetParentChatId(),
			Agent:    i.GetAgent(),
			Label:    i.GetLabel(),
			State:    i.GetState(),
			Readonly: i.GetReadonly(),
			Depth:    int(n.GetDepth()),
			Self:     n.GetSelf(),
		})
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

// maxWaitIDs bounds the ids of one wait.
const maxWaitIDs = 64

func (s *statusServer) handleSubagentWait(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentWaitRequest
	if !decodeCall(w, r, &req) {
		return
	}
	if len(req.IDs) > maxWaitIDs || req.TimeoutMs < 0 {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "too many subagent ids or a negative timeout"})
		return
	}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		id = strings.TrimSpace(clean(id))
		if id == "" || len(id) > maxMessageID {
			writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a valid subagent id is required"})
			return
		}
		ids = append(ids, id)
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.WaitSubagentsResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.WaitSubagents(ctx, &runnerpb.WaitSubagentsRequest{Meta: s.meta(seq), Ids: ids, Any: req.Any, TimeoutMs: req.TimeoutMs})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-wait", err)
		return
	}
	out := sandboxsock.SubagentWaitResponse{Results: []sandboxsock.SubagentWaitResult{}}
	for _, res := range resp.GetResults() {
		out.Results = append(out.Results, sandboxsock.SubagentWaitResult{
			Subagent:     subagentInfoJSON(res.GetSubagent()),
			StillRunning: res.GetStillRunning(),
			Notes:        res.GetNotes(),
		})
	}
	out.CheckoutHeldBy = resp.GetCheckoutHeldBy()
	if len(resp.GetWokenBy()) > 0 {
		out.WokenByMessage = true
		out.MessageFrom = resp.GetWokenBy()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *statusServer) handleSubagentReply(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentReplyRequest
	if !decodeCall(w, r, &req) {
		return
	}
	text := clean(req.Text)
	if strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a message is required"})
		return
	}
	if len(text) > maxCallText {
		http.Error(w, "message too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.ReplyToParentResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.ReplyToParent(ctx, &runnerpb.ReplyToParentRequest{Meta: s.meta(seq), Text: text})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-reply", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentReplyResponse{Delivery: resp.GetDelivery()})
}

func (s *statusServer) handleSubagentAsk(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentAskRequest
	if !decodeCall(w, r, &req) {
		return
	}
	to, resume, text := clean(req.To), clean(req.ResumeQID), clean(req.Text)
	if len(to) > maxMessageID || len(resume) > maxMessageID || req.TimeoutMs < 0 {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "invalid chat id, question id or timeout"})
		return
	}
	switch {
	case resume != "" && (to != "" || text != ""):
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "resume takes a question id only"})
		return
	case resume == "" && (to == "" || strings.TrimSpace(text) == ""):
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a chat id and a question are required"})
		return
	}
	if len(text) > maxCallText {
		http.Error(w, "question too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.AskSubagentResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.AskSubagent(ctx, &runnerpb.AskSubagentRequest{Meta: s.meta(seq), To: to, Text: text, TimeoutMs: req.TimeoutMs, ResumeQid: resume})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-ask", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentAskResponse{QID: resp.GetQid(), State: resp.GetState(), Answer: resp.GetAnswer()})
}

func (s *statusServer) handleSubagentAnswer(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentAnswerRequest
	if !decodeCall(w, r, &req) {
		return
	}
	qid, text := clean(req.QID), clean(req.Text)
	if qid == "" || len(qid) > maxMessageID || strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a valid question id and an answer are required"})
		return
	}
	if len(text) > maxCallText {
		http.Error(w, "answer too large", http.StatusRequestEntityTooLarge)
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.AnswerSubagentResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.AnswerSubagent(ctx, &runnerpb.AnswerSubagentRequest{Meta: s.meta(seq), Qid: qid, Text: text})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-answer", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentAnswerResponse{Delivery: resp.GetDelivery()})
}

func questionInfoJSON(q *runnerpb.SubagentQuestion) sandboxsock.SubagentQuestionInfo {
	return sandboxsock.SubagentQuestionInfo{
		QID:        q.GetQid(),
		FromChatID: q.GetFromChatId(),
		FromAgent:  q.GetFromAgent(),
		FromLabel:  q.GetFromLabel(),
		CreatedMs:  q.GetCreatedUnixMs(),
		FirstLine:  q.GetFirstLine(),
		ToChatID:   q.GetToChatId(),
		State:      q.GetState(),
	}
}

func (s *statusServer) handleSubagentQuestions(w http.ResponseWriter, r *http.Request) {
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.SubagentQuestionsResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.SubagentQuestions(ctx, &runnerpb.SubagentQuestionsRequest{Meta: s.meta(seq)})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-questions", err)
		return
	}
	out := sandboxsock.SubagentQuestionsResponse{Incoming: []sandboxsock.SubagentQuestionInfo{}, Outgoing: []sandboxsock.SubagentQuestionInfo{}}
	for _, q := range resp.GetIncoming() {
		out.Incoming = append(out.Incoming, questionInfoJSON(q))
	}
	for _, q := range resp.GetOutgoing() {
		out.Outgoing = append(out.Outgoing, questionInfoJSON(q))
	}
	writeJSON(w, http.StatusOK, out)
}
