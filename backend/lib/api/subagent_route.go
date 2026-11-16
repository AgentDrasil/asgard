package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// This file is the routing table behind `subagent route add|rm|list` and
// `subagent ask --topic`: it tells an agent whom to ask about what. An entry
// maps a topic to one chat instance (a chat id, never a role: the same role
// can appear several times in one flow). Entries are stored per chat in the
// database (dbmodels.ChatRoute) and survive a backend restart.
//
// The table a chat sees is the union of its own entries and those of its
// ancestors up to the root; for a topic defined more than once the nearest chat
// wins. There is one implicit entry, `user`, which resolves to the root chat of
// the tree; an explicit `user` entry overrides it. A chat only ever writes to
// its own entries, and its identity is the runner-stamped one.

const (
	// maxRoutesPerChat caps the entries stored on one chat.
	maxRoutesPerChat = 32
	// maxRouteNoteBytes caps the note of an entry.
	maxRouteNoteBytes = 200
	// routeTopicUser is the implicit topic that resolves to the tree's root.
	routeTopicUser = "user"
	// routeDefaultOrigin is the DefinedAt of the implicit entry.
	routeDefaultOrigin = "(default)"
	// routeStateGone is the state of an entry whose target chat is gone.
	routeStateGone = "gone"
	// routeStateArchived is the state of an entry whose target chat is archived:
	// it cannot be asked.
	routeStateArchived = "archived"
	// escalatedAskPrefix marks a question that came from an escalated ask-user.
	escalatedAskPrefix = "(escalated ask-user) "
)

var routeTopicRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,63}$`)

// RouteEntry is one row of an effective routing table.
type RouteEntry struct {
	Topic string
	// To is the target chat id; Agent, Label and State describe it.
	To, Agent, Label, State string
	// DefinedAt is the chat that stores the entry, or routeDefaultOrigin.
	DefinedAt string
	Note      string
	// Self is true when To is the chat the table was computed for.
	Self bool
}

func validRouteTopic(topic string) error {
	if !routeTopicRE.MatchString(topic) {
		return subagentErr(SubagentInvalid, "invalid topic %q: use lowercase letters, digits and . _ : - (1 to 64 characters, starting with a letter or digit)", topic)
	}
	return nil
}

// AddRoute stores topic -> to on callerChat, replacing the entry the chat
// already has for that topic. to must be a chat of the caller's spawn tree (the
// caller itself is allowed).
func (s *Server) AddRoute(callerChat, topic, to, note string) (replaced bool, err error) {
	if s.repo == nil {
		return false, subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if err := validRouteTopic(topic); err != nil {
		return false, err
	}
	if len(note) > maxRouteNoteBytes {
		return false, subagentErr(SubagentInvalid, "note too long (limit %d bytes)", maxRouteNoteBytes)
	}
	if strings.ContainsFunc(note, unicode.IsControl) {
		return false, subagentErr(SubagentInvalid, "the note must be a single line without control characters")
	}
	if !IsValidChatID(to) {
		return false, subagentErr(SubagentInvalid, "invalid chat id")
	}
	caller, err := s.repo.GetSessionMeta(callerChat)
	if err != nil {
		return false, subagentErr(SubagentInternal, "failed to get session: %v", err)
	}
	if caller == nil {
		return false, subagentErr(SubagentNotFound, "caller chat not found")
	}
	if to != callerChat {
		if _, err := s.treeMember(caller, to); err != nil {
			return false, err
		}
	}
	replaced, err = s.repo.SetRoute(callerChat, topic, to, note, maxRoutesPerChat)
	if errors.Is(err, dbmodels.ErrRouteLimit) {
		return false, subagentErr(SubagentLimit, "too many routes on this chat (limit %d); remove one with `subagent route rm --topic <t>`", maxRoutesPerChat)
	}
	if err != nil {
		return false, subagentErr(SubagentInternal, "failed to store the route: %v", err)
	}
	return replaced, nil
}

// RemoveRoute removes the entry of topic on callerChat. Entries of ancestors
// are never touched.
func (s *Server) RemoveRoute(callerChat, topic string) error {
	if s.repo == nil {
		return subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if err := validRouteTopic(topic); err != nil {
		return err
	}
	err := s.repo.DeleteRoute(callerChat, topic)
	if errors.Is(err, dbmodels.ErrRouteNotFound) {
		return subagentErr(SubagentNotFound, "your chat has no route for topic %q (routes of ancestors can only be removed by their own chat; see `subagent route list`)", topic)
	}
	if err != nil {
		return subagentErr(SubagentInternal, "failed to remove the route: %v", err)
	}
	return nil
}

// EffectiveRoutes is the routing table callerChat sees, sorted by topic: its
// own entries and those of its ancestors (nearest wins) plus the implicit
// `user` entry (the root chat) unless an entry defines it. It costs one
// ancestor query, one query for the routes of those chats and one for the
// targets.
func (s *Server) EffectiveRoutes(callerChat string) ([]RouteEntry, error) {
	if s.repo == nil {
		return nil, subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	routes, root, err := s.repo.EffectiveRoutes(callerChat)
	if err != nil {
		return nil, subagentErr(SubagentInternal, "failed to read the routing table: %v", err)
	}
	if root == "" {
		return nil, subagentErr(SubagentNotFound, "caller chat not found")
	}
	ids := []string{root}
	for _, r := range routes {
		ids = append(ids, r.TargetChatID)
	}
	metas, err := s.repo.SessionsMeta(ids)
	if err != nil {
		return nil, subagentErr(SubagentInternal, "failed to read the routing targets: %v", err)
	}
	entry := func(topic, to, definedAt, note string) RouteEntry {
		e := RouteEntry{Topic: topic, To: to, DefinedAt: definedAt, Note: note, Self: to == callerChat, State: routeStateGone}
		if sess := metas[to]; sess != nil {
			info := s.subagentInfo(sess)
			e.Agent, e.Label, e.State = info.Agent, info.Label, info.State
			if sess.ParentChatID == "" {
				// A root is no subagent: its spawn flags mean nothing and its
				// state is running, queued or idle, as in `subagent tree`.
				e.Label = ""
				if e.State != SubagentStateRunning && e.State != SubagentStateQueued {
					e.State = SubagentStateIdle
				}
			}
			if sess.IsArchived {
				e.State = routeStateArchived
			}
		}
		return e
	}
	out := make([]RouteEntry, 0, len(routes)+1)
	hasUser := false
	for _, r := range routes {
		if r.Topic == routeTopicUser {
			hasUser = true
		}
		out = append(out, entry(r.Topic, r.TargetChatID, r.DefinedAt, r.Note))
	}
	if !hasUser {
		out = append(out, entry(routeTopicUser, root, routeDefaultOrigin, ""))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out, nil
}

// resolveRoute finds topic in callerChat's effective table. An unknown topic is
// a not-found error that lists the available ones. The result may be the
// caller itself (see ResolveTopic).
func (s *Server) resolveRoute(callerChat, topic string) (RouteEntry, error) {
	if err := validRouteTopic(topic); err != nil {
		return RouteEntry{}, err
	}
	table, err := s.EffectiveRoutes(callerChat)
	if err != nil {
		return RouteEntry{}, err
	}
	for _, e := range table {
		if e.Topic == topic {
			if e.State == routeStateGone {
				return RouteEntry{}, subagentErr(SubagentNotFound, "the chat %s for topic %q no longer exists; pick another with `subagent route list` or `subagent tree`", e.To, topic)
			}
			if e.State == routeStateArchived && !e.Self {
				return RouteEntry{}, subagentErr(SubagentUnavailable, "target %s for topic %q is archived; route the topic to a live chat with `subagent route add` (current table: `subagent route list`)", e.To, topic)
			}
			return e, nil
		}
	}
	topics := make([]string, len(table))
	for i, e := range table {
		topics[i] = e.Topic
	}
	return RouteEntry{}, subagentErr(SubagentNotFound, "no route for topic %q; available topics: %s (see `subagent route list`)", topic, strings.Join(topics, ", "))
}

// ResolveTopic resolves a topic for `subagent ask --topic`. It fails when the
// topic resolves to callerChat itself: the caller is then the one who has to
// decide.
func (s *Server) ResolveTopic(callerChat, topic string) (RouteEntry, error) {
	e, err := s.resolveRoute(callerChat, topic)
	if err != nil {
		return RouteEntry{}, err
	}
	if e.Self {
		return RouteEntry{}, subagentErr(SubagentInvalid, "you are the route for %s; decide yourself", topic)
	}
	return e, nil
}

// AskTopic is AskSubagent addressed through the routing table: it resolves
// topic for callerChat, then behaves exactly like AskSubagent to the result,
// which it also returns so the caller can say whom it asked.
func (s *Server) AskTopic(ctx context.Context, callerChat, callerAgent, topic, text string, timeout time.Duration) (AskOutcome, RouteEntry, error) {
	target, err := s.ResolveTopic(callerChat, topic)
	if err != nil {
		return AskOutcome{}, RouteEntry{}, err
	}
	out, err := s.AskSubagent(ctx, callerChat, callerAgent, target.To, text, timeout)
	if err != nil {
		// Say whom the topic resolved to, so a refusal is not anonymous.
		var se *SubagentError
		if errors.As(err, &se) {
			err = subagentErr(se.Kind, "topic %s -> %s (%s): %s", topic, target.To, routeWho(target), se.Msg)
		}
	}
	return out, target, err
}

// oneLine makes s safe to put on one line of the injected block: control
// characters (newlines included) become spaces, so a label or note cannot forge
// the block's delimiters (`---`) or add lines.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	// Nor may it contain the block's own delimiter.
	return strings.ReplaceAll(s, "---", "- - -")
}

// routeWho is "agent/label" of an entry.
func routeWho(e RouteEntry) string {
	if e.Label != "" {
		return e.Agent + "/" + e.Label
	}
	return e.Agent
}

// routeLine renders one entry for the block injected into a spawn prompt.
func routeLine(e RouteEntry) string {
	who := "(" + oneLine(e.Agent)
	if e.Label != "" {
		who += "/" + oneLine(e.Label)
	}
	who += ")"
	switch e.State {
	case routeStateGone:
		who = "(gone)"
	case routeStateArchived:
		who = "(archived)"
	}
	line := fmt.Sprintf("  %s -> %s %s", oneLine(e.Topic), oneLine(e.To), who)
	if e.DefinedAt != routeDefaultOrigin {
		line += " via " + oneLine(e.DefinedAt)
	} else {
		line += " (default)"
	}
	if e.Note != "" {
		line += " - " + oneLine(e.Note)
	}
	return line
}

// routingPromptBlock is appended to the first prompt of a spawned child: its
// own chat id and its parent's, and the routing table as the child sees it at
// spawn time. A failed table read is logged and leaves the table out.
func (s *Server) routingPromptBlock(childID, parentID string) string {
	var b strings.Builder
	b.WriteString("\n\n--- asgard routing (set at spawn; `subagent route list` is the current table) ---\n")
	fmt.Fprintf(&b, "your chat id: %s\nparent chat id: %s\n", oneLine(childID), oneLine(parentID))
	table, err := s.EffectiveRoutes(childID)
	if err != nil {
		log.Warn().Err(err).Str("chat_id", childID).Msg("could not read the routing table for the spawn prompt")
	} else {
		b.WriteString("routes (topic -> chat):\n")
		for _, e := range table {
			b.WriteString(routeLine(e))
			b.WriteByte('\n')
		}
	}
	b.WriteString("ask a topic: subagent ask --topic <t> -- \"<q>\"   current table: subagent route list\n")
	b.WriteString("--- end routing ---")
	return b.String()
}

// askUserAction is what a sandbox ask-user call does.
type askUserAction int

const (
	askUserAllow askUserAction = iota
	askUserDeny
	askUserEscalate
)

// askUserDecision is the outcome of askUserDecide.
type askUserDecision struct {
	action askUserAction
	// msg is the refusal of a deny.
	msg string
	// target is the chat a question is escalated to.
	target RouteEntry
}

// askUserDecide is the one ask_user policy check, shared by the sandbox
// callback and POST /api/ask-user. The policy is the config's (nil when the
// agent is unknown), defaulting to allow for a root chat and escalate for a
// subagent chat; an escalate resolves the `user` topic of the chat's routing
// table: another chat gets the question, and when it is the chat itself a root
// chat falls back to allow and a subagent chat is denied. If the chat cannot be
// read it fails closed, unless the config explicitly allows. A chat that does
// not exist counts as a root chat. Workflow human nodes and quota suspension
// write ask_user messages without this check.
func (s *Server) askUserDecide(chatID string, cfg *agentspec.AgentConfig) askUserDecision {
	isSub := false
	if s.repo != nil {
		sess, err := s.repo.GetSessionMeta(chatID)
		if err != nil {
			log.Error().Err(err).Str("chat_id", chatID).Msg("ask-user: cannot read chat to resolve ask_user policy")
			if cfg != nil && cfg.AskUser == agentspec.AskUserAllow {
				return askUserDecision{action: askUserAllow}
			}
			return askUserDecision{action: askUserDeny, msg: "could not determine chat; ask-user refused"}
		}
		isSub = sess != nil && sess.ParentChatID != ""
	}
	switch cfg.EffectiveAskUser(isSub) {
	case agentspec.AskUserAllow:
		return askUserDecision{action: askUserAllow}
	case agentspec.AskUserDeny:
		return askUserDecision{action: askUserDeny, msg: askUserDenyMessage(isSub, cfg, "")}
	}
	target, err := s.resolveRoute(chatID, routeTopicUser)
	if err != nil {
		var se *SubagentError
		if !isSub && errors.As(err, &se) && se.Kind == SubagentNotFound {
			return askUserDecision{action: askUserAllow}
		}
		log.Error().Err(err).Str("chat_id", chatID).Msg("ask-user: cannot resolve the user route")
		return askUserDecision{action: askUserDeny, msg: "could not resolve the `user` route (" + err.Error() + "); ask-user refused. Ask a live chat with `subagent ask --topic <t>` (topics: `subagent route list`)"}
	}
	if target.Self {
		if !isSub {
			return askUserDecision{action: askUserAllow}
		}
		return askUserDecision{action: askUserDeny, msg: askUserDenyMessage(isSub, cfg, "the `user` route resolves to this chat")}
	}
	return askUserDecision{action: askUserEscalate, target: target}
}

// askUserDenyMessage is the error of a denied ask-user. why is set when an
// escalation had nowhere to go.
func askUserDenyMessage(isSub bool, cfg *agentspec.AgentConfig, why string) string {
	if !isSub {
		return "ask-user is disabled for this agent (ask_user: deny). Do not wait for the user; proceed with your best judgment or state the open question in your final answer."
	}
	reason := "ask-user is disabled for this agent (ask_user: deny). "
	switch {
	case why != "":
		reason = "ask-user is disabled for this agent (ask_user: escalate, but " + why + "). "
		if cfg == nil || cfg.AskUser == "" {
			reason = "ask-user is disabled for this agent (subagents default to deny when " + why + "). "
		}
	case cfg == nil || cfg.AskUser == "":
		reason = "ask-user is disabled for this agent (ask_user: deny; subagents default to deny). "
	}
	return reason + "Do not wait for the user. If you need an answer, ask the chat that owns the topic with `subagent ask --topic <t> -- <question>` (current topics: `subagent route list`) or a chat by id with `subagent ask --to <chat-id>` (ids: `subagent tree`); or end your turn with the question as your final output (your parent reads it via `subagent wait`/`status`), or send a non-blocking `subagent reply`."
}

// escalateAskUser forwards an ask-user question to the chat the `user` topic
// resolved to (target) like `subagent ask --to <target>` and blocks up to
// defaultAskWait (90s, below the 120s kill of the usual agent CLIs' command
// tool; a longer wait is `subagent ask --resume`). It returns the answer; when none arrives in time the error
// names the question so the caller can resume it.
func (s *Server) escalateAskUser(ctx context.Context, chatID, agentID string, target RouteEntry, question string) (string, error) {
	wait := defaultAskWait
	if s.askUserEscalateWait > 0 {
		wait = s.askUserEscalateWait
	}
	out, err := s.AskSubagent(ctx, chatID, agentID, target.To, escalatedAskPrefix+question, wait)
	if err != nil {
		return "", err
	}
	if !out.Answered {
		return "", fmt.Errorf("no answer yet from %s; qid %s; continue with: subagent ask --resume %s", target.To, out.QID, out.QID)
	}
	return out.Answer, nil
}
