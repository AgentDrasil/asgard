package api

import (
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// EventTypeSubagentArtifact is published on the stream of every ancestor of a
// subagent chat when that chat records artifacts, so the ancestor's artifact
// list shows what its descendants produce without opening them. Payload:
// {id, label, agent, artifacts}.
const EventTypeSubagentArtifact = "subagent_artifact"

// SubagentArtifacts is the artifacts one descendant subagent chat produced.
// The viewer opens them with ChatID as the session id: /tmp and /session paths
// are the subagent's own, /shared paths are the tree's.
type SubagentArtifacts struct {
	ChatID    string   `json:"chatId"`
	Label     string   `json:"label"`
	Agent     string   `json:"agent,omitempty"`
	Artifacts []string `json:"artifacts"`
	// Truncated: the chat has more artifacts than listed (see maxArtifactsPerSubagent).
	Truncated bool `json:"truncated,omitempty"`
}

// maxArtifactsPerSubagent caps the artifacts listed per descendant in a
// session response; Truncated says when it applied (the newest are kept).
const maxArtifactsPerSubagent = 500

// subagentArtifactLabel is how a subagent chat is named to the user.
func subagentArtifactLabel(sess *dbmodels.Session) string {
	if sess.SpawnLabel != "" {
		return sess.SpawnLabel
	}
	return strings.TrimSpace(sess.Title)
}

// descendantArtifacts lists the artifacts of every descendant of chatID (not
// chatID itself), per chat, oldest chat first. Chats without artifacts are left
// out. A failing lookup yields nothing: the list is a convenience on top of the
// chat's own artifacts.
func (s *Server) descendantArtifacts(chatID string) []SubagentArtifacts {
	if s.repo == nil {
		return nil
	}
	members, _, err := s.repo.SpawnDescendants(chatID, maxSubagentDescendants)
	if err != nil {
		return nil
	}
	var out []SubagentArtifacts
	for i := range members {
		m := &members[i]
		if m.ChatID == chatID || len(m.Artifacts) == 0 {
			continue
		}
		arts, truncated := m.Artifacts, false
		if len(arts) > maxArtifactsPerSubagent {
			arts, truncated = arts[len(arts)-maxArtifactsPerSubagent:], true
		}
		out = append(out, SubagentArtifacts{
			ChatID:    m.ChatID,
			Label:     subagentArtifactLabel(m),
			Agent:     m.CurrentAgent,
			Artifacts: append([]string(nil), arts...),
			Truncated: truncated,
		})
	}
	return out
}

// publishSubagentArtifacts tells every ancestor of a subagent chat that the
// chat recorded files as artifacts. It does nothing for a chat without parent.
func (s *Server) publishSubagentArtifacts(sess *dbmodels.Session, files []string) {
	if s == nil || s.repo == nil || s.eventHub == nil || sess == nil || sess.ParentChatID == "" || len(files) == 0 {
		return
	}
	ancestors, err := s.subagentAncestors(sess)
	if err != nil {
		return
	}
	payload := map[string]any{
		"id":        sess.ChatID,
		"label":     subagentArtifactLabel(sess),
		"agent":     sess.CurrentAgent,
		"artifacts": files,
	}
	for _, a := range ancestors {
		s.PublishSessionEvent(a, SessionEvent{Type: EventTypeSubagentArtifact, Payload: payload})
	}
}

// chatHasShared reports whether the chat runs with a /shared directory (see
// SingleAgentExecutor.sharedChat): a subagent, a chat with subagents, or a chat
// of a team agent (it may spawn). The team is looked up from the chat's
// current agent on its runner; the executor decides from the agent that runs,
// which differs only while a chat's agent is being switched.
func (s *Server) chatHasShared(sess *dbmodels.Session) bool {
	if sess == nil {
		return false
	}
	if sess.ParentChatID != "" {
		return true
	}
	if s.repo != nil {
		if kids, err := s.repo.ChildChatIDs(sess.ChatID); err == nil && len(kids) > 0 {
			return true
		}
	}
	if a, err := s.findAgentFor(sess.RunnerNodeID, sess.CurrentAgent); err == nil && a != nil {
		return strings.TrimSpace(a.Config.Team) != ""
	}
	return false
}
