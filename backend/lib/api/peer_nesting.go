package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	optional "github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// maxPeerCallDepth bounds how many agents may wait on each other in one
// call-peer chain of a chat (the root caller included).
const maxPeerCallDepth = 4

// peerCallKey carries the *peerCall of a call-peer through the context.
type peerCallKey struct{}

// peerCall describes a call-peer request: the caller's run holds the chat's
// execution guard (holder) and is blocked until the peer returns.
type peerCall struct {
	chatID        string
	holder        any // value of activeExecutions[chatID] when the call began
	callerAgentID string
}

// nestedPeerKey marks the execution context of a nested call-peer run.
type nestedPeerKey struct{}

func isNestedPeerRun(ctx context.Context) bool {
	return ctx != nil && ctx.Value(nestedPeerKey{}) != nil
}

func peerCallFromContext(ctx context.Context) *peerCall {
	if ctx == nil {
		return nil
	}
	pc, _ := ctx.Value(peerCallKey{}).(*peerCall)
	return pc
}

// enterPeerCall records target as the next agent of the chat's call-peer
// chain. The caller must be the agent at the end of the chain (the chain starts
// at the run holding the guard), which also rejects a run that already waits
// on a peer. It fails on a cycle or when the chain would get too deep.
func (s *Server) enterPeerCall(chatID, callerAgentID, targetAgentID string) error {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	chain := s.peerChains[chatID]
	if len(chain) == 0 {
		chain = []string{callerAgentID}
	} else if chain[len(chain)-1] != callerAgentID {
		return fmt.Errorf("agent %q is already waiting on a peer call in this chat", callerAgentID)
	}
	for _, id := range chain {
		if id == targetAgentID {
			return fmt.Errorf("call-peer cycle: agent %q is already on the call chain %s", targetAgentID, strings.Join(append(append([]string(nil), chain...), targetAgentID), " -> "))
		}
	}
	if len(chain) >= maxPeerCallDepth {
		return fmt.Errorf("call-peer nesting too deep: the call chain %s already has %d agents (limit %d)", strings.Join(chain, " -> "), len(chain), maxPeerCallDepth)
	}
	if s.peerChains == nil {
		s.peerChains = map[string][]string{}
	}
	s.peerChains[chatID] = append(chain[:len(chain):len(chain)], targetAgentID)
	return nil
}

// peerChainRoot is the agent whose execution holds the chat's guard: the head
// of the active call chain, or the caller itself when no peer call is active.
func (s *Server) peerChainRoot(chatID, callerAgentID string) string {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if chain := s.peerChains[chatID]; len(chain) > 0 {
		return chain[0]
	}
	return callerAgentID
}

// leavePeerCall undoes enterPeerCall.
func (s *Server) leavePeerCall(chatID string) {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	chain := s.peerChains[chatID]
	if len(chain) <= 2 {
		delete(s.peerChains, chatID)
		return
	}
	s.peerChains[chatID] = chain[:len(chain)-1]
}

// triggerNestedPeer runs a call-peer target inside the chat of the run that
// called it. That run holds the chat's guard but is blocked waiting for this
// peer, so the chat is not truly busy: the peer is admitted without taking the
// guard, and a different holder still gets the usual 409. The peer's context
// derives from the holder's execution, so stopping the chat stops the peer.
// It returns false when the chat has no guard (the normal path then applies).
func (s *Server) triggerNestedPeer(stopWith context.Context, w http.ResponseWriter, pc *peerCall, target *agentspec.Agent, runnerID, chatID string, req TriggerMessageRequest) bool {
	cur, running := s.activeExecutions.Load(chatID)
	if !running {
		return false
	}
	// The holder must be the caller's own plain agent execution; anything else
	// (another execution, a workflow, a placeholder) is an unrelated busy chat.
	h, isHandle := cur.(*executionHandle)
	if cur != pc.holder || !isHandle || h.isWorkflow || h.agentID != s.peerChainRoot(chatID, pc.callerAgentID) {
		http.Error(w, `{"error":"session is already running a task"}`, http.StatusConflict)
		return true
	}
	if target.Config.Type == "workflow" {
		writeJSONError(w, http.StatusBadRequest, "call-peer into the caller's own chat supports agents only, not workflows")
		return true
	}
	if !s.requireRunnerOnline(w, runnerID) {
		return true
	}
	if err := s.enterPeerCall(chatID, pc.callerAgentID, target.Config.ID); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return true
	}
	defer s.leavePeerCall(chatID)

	base := s.Context()
	if h.ctx != nil {
		base = h.ctx
	}
	execCtx, cancel := context.WithCancel(base)
	defer cancel()
	defer cancelWhenDone(stopWith, cancel)()
	execCtx = context.WithValue(execCtx, nestedPeerKey{}, true)
	runDirOpt := optional.None[string]()
	if req.RunDir != "" {
		runDirOpt = optional.Some(req.RunDir)
	}
	allowOpt := optional.None[bool]()
	if req.AllowCrossSession != nil {
		allowOpt = optional.Some(*req.AllowCrossSession)
	}
	if s.repo != nil {
		if err := s.repo.UpdateAgentSession(chatID, target.Config.ID, "", "", runDirOpt, allowOpt); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on nested peer call")
		}
	}
	status, output, err := s.executeSingleAgent(execCtx, target, chatID, req)
	resp := map[string]any{"status": status, "chatId": chatID}
	code := http.StatusOK
	if err != nil {
		code = http.StatusInternalServerError
		resp["error"] = err.Error()
	} else {
		resp["output"] = output
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
	return true
}
