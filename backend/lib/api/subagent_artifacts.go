package api

import (
	"context"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
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

// isInternalArtifactPath reports whether a path in /session or /tmp is an
// internal runtime/sandbox file managed by Asgard or the agent CLI rather than
// a user-visible artifact.
func isInternalArtifactPath(p string) bool {
	switch p {
	case "/session/AW_AGENTS.md", "/session/messages.jsonl",
		"/tmp/.aw_agents.md", "/tmp/asgard-ssh-agent.sock":
		return true
	}
	for _, prefix := range []string{
		"/session/workflows/",
		"/tmp/fakebash-outputs/",
		"/tmp/cc-socks/",
		"/tmp/.asgard-ca/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	// Claude Code runtime state dir (/tmp/claude-<uid>/...), without hiding
	// user files like /tmp/claude-notes.md.
	if rest, ok := strings.CutPrefix(p, "/tmp/claude-"); ok {
		if slash := strings.IndexByte(rest, '/'); slash > 0 {
			digits := rest[:slash]
			allDigits := true
			for i := 0; i < len(digits); i++ {
				if digits[i] < '0' || digits[i] > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return true
			}
		}
	}
	return false
}

// namespaceFiles lists the files currently present in a scoped namespace
// ("shared", "session", or "tmp") via fs.Search (best-effort; returns nil on error).
func namespaceFiles(ctx context.Context, fs runnerfs.FS, scope string) []string {
	if fs == nil {
		return nil
	}
	hits, err := fs.Search(ctx, runnerfs.SearchRequest{
		Limit: runnerfs.MaxSearchLimit,
		Scope: scope,
	})
	if err != nil || len(hits) == 0 {
		return nil
	}
	root := "/" + scope
	prefix := root + "/"
	seen := make(map[string]struct{}, len(hits))
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.Scope != scope {
			continue
		}
		if h.Path != root && !strings.HasPrefix(h.Path, prefix) {
			continue
		}
		if scope != agents.SharedNS && isInternalArtifactPath(h.Path) {
			continue
		}
		if _, dup := seen[h.Path]; dup {
			continue
		}
		seen[h.Path] = struct{}{}
		out = append(out, h.Path)
	}
	return out
}

// discoverSessionArtifacts lists files currently present in /shared (when
// hasShared is true), /session, and /tmp for sess (best-effort).
func (s *Server) discoverSessionArtifacts(ctx context.Context, sess *dbmodels.Session, hasShared bool) []string {
	if s == nil || sess == nil {
		return nil
	}
	var out []string
	if hasShared {
		if sfs, err := s.sharedFS(sess.ChatID, sess); err == nil {
			out = append(out, namespaceFiles(ctx, sfs, agents.SharedNS)...)
		}
	}
	if cfs, err := s.chatFS(sess.ChatID); err == nil {
		out = append(out, namespaceFiles(ctx, cfs, "session")...)
		out = append(out, namespaceFiles(ctx, cfs, "tmp")...)
	}
	return out
}

// syncSessionArtifacts discovers files written to /shared, /session, and /tmp
// by tools that do not emit target_files (e.g. Bash redirections), records any
// not yet claimed onto chatID, broadcasts artifact events, and returns the
// newly recorded paths.
func (s *Server) syncSessionArtifacts(ctx context.Context, chatID string) []string {
	if s == nil || s.repo == nil || chatID == "" {
		return nil
	}
	sess, err := s.repo.GetSessionMeta(chatID)
	if err != nil || sess == nil {
		return nil
	}
	hasShared := s.chatHasShared(sess)
	files := s.discoverSessionArtifacts(ctx, sess, hasShared)
	if len(files) == 0 {
		return nil
	}
	existing := make(map[string]struct{}, len(sess.Artifacts))
	for _, a := range sess.Artifacts {
		existing[a] = struct{}{}
	}
	if hasShared {
		for _, a := range s.sharedTreeArtifacts(sess, nil) {
			existing[a] = struct{}{}
		}
	}
	var newFiles []string
	for _, f := range files {
		if _, ok := existing[f]; !ok {
			existing[f] = struct{}{}
			newFiles = append(newFiles, f)
		}
	}
	if len(newFiles) == 0 {
		return nil
	}
	added, err := s.repo.AppendNewArtifacts(chatID, newFiles)
	if err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to sync session artifacts to repo")
		return nil
	}
	if len(added) == 0 {
		return nil
	}
	s.PublishSessionEvent(chatID, SessionEvent{
		Type:    EventTypeArtifact,
		Payload: map[string]any{"artifacts": added},
	})
	s.publishSubagentArtifacts(sess, added)
	return added
}
