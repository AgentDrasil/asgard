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
	mux.HandleFunc("POST "+sandboxsock.PathSubagentRouteAdd, s.handleSubagentRouteAdd)
	mux.HandleFunc("POST "+sandboxsock.PathSubagentRouteRm, s.handleSubagentRouteRm)
	mux.HandleFunc("GET "+sandboxsock.PathSubagentRouteList, s.handleSubagentRouteList)
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
	to, topic, resume, text := clean(req.To), clean(req.Topic), clean(req.ResumeQID), clean(req.Text)
	if len(to) > maxMessageID || len(topic) > maxTopic || len(resume) > maxMessageID || req.TimeoutMs < 0 {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "invalid chat id, topic, question id or timeout"})
		return
	}
	switch {
	case resume != "" && (to != "" || topic != "" || text != ""):
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "resume takes a question id only"})
		return
	case resume == "" && to != "" && topic != "":
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a chat id and a topic are exclusive"})
		return
	case resume == "" && ((to == "" && topic == "") || strings.TrimSpace(text) == ""):
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a chat id or a topic, and a question are required"})
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
		resp, err = cb.AskSubagent(ctx, &runnerpb.AskSubagentRequest{Meta: s.meta(seq), To: to, Topic: topic, Text: text, TimeoutMs: req.TimeoutMs, ResumeQid: resume})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-ask", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentAskResponse{
		QID: resp.GetQid(), State: resp.GetState(), Answer: resp.GetAnswer(),
		Topic: resp.GetTopic(), TargetID: resp.GetTargetChatId(), TargetAgent: resp.GetTargetAgent(), TargetLabel: resp.GetTargetLabel(),
	})
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

// maxTopic bounds a routing topic (the backend enforces its syntax).
const maxTopic = 64

// maxRouteNote bounds a routing note (the backend enforces the same limit).
const maxRouteNote = 200

func (s *statusServer) handleSubagentRouteAdd(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentRouteAddRequest
	if !decodeCall(w, r, &req) {
		return
	}
	topic, to, note := clean(req.Topic), clean(req.To), clean(req.Note)
	if topic == "" || to == "" {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a topic and a chat id are required"})
		return
	}
	if len(topic) > maxTopic || len(to) > maxMessageID || len(note) > maxRouteNote {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "topic, chat id or note too long"})
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.AddSubagentRouteResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.AddSubagentRoute(ctx, &runnerpb.AddSubagentRouteRequest{Meta: s.meta(seq), Topic: topic, To: to, Note: note})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-route-add", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentRouteAddResponse{Replaced: resp.GetReplaced()})
}

func (s *statusServer) handleSubagentRouteRm(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.SubagentRouteRmRequest
	if !decodeCall(w, r, &req) {
		return
	}
	topic := clean(req.Topic)
	if topic == "" || len(topic) > maxTopic {
		writeJSON(w, http.StatusBadRequest, sandboxsock.ErrorResponse{Error: "a valid topic is required"})
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) error {
		_, err := cb.RemoveSubagentRoute(ctx, &runnerpb.RemoveSubagentRouteRequest{Meta: s.meta(seq), Topic: topic})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-route-rm", err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxsock.SubagentRouteRmResponse{Removed: true})
}

func (s *statusServer) handleSubagentRouteList(w http.ResponseWriter, r *http.Request) {
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	var resp *runnerpb.ListSubagentRoutesResponse
	err := s.callBackend(ctx, func(ctx context.Context, cb runnerpb.AgentCallbackServiceClient) (err error) {
		resp, err = cb.ListSubagentRoutes(ctx, &runnerpb.ListSubagentRoutesRequest{Meta: s.meta(seq)})
		return err
	})
	if err != nil {
		s.failSubagent(w, ctx, "subagent-route-list", err)
		return
	}
	out := sandboxsock.SubagentRouteListResponse{Routes: []sandboxsock.SubagentRouteInfo{}}
	for _, e := range resp.GetRoutes() {
		out.Routes = append(out.Routes, sandboxsock.SubagentRouteInfo{
			Topic: e.GetTopic(), To: e.GetToChatId(), Agent: e.GetAgent(), Label: e.GetLabel(), State: e.GetState(),
			DefinedAt: e.GetDefinedAt(), Note: e.GetNote(), Self: e.GetSelf(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
