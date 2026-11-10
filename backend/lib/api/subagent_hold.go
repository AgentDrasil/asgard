package api

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
)

// This file keeps the CLI of a parent chat alive while its subagents live.
//
// Without it a claude/agy parent's process exits at the end of its turn, and
// the finish notice of every child starts a whole new run of the parent. With
// it the backend tells the run (Hub.ControlRun -> runner -> aw) to hold its
// stdin open ("hold") while the chat has live children and to let go
// ("release") when the last one is done, so notices are injected as new turns
// of the same process.
//
// Lifecycle of the hold of a chat (holdState), all serialized by holdMu:
//   - syncHold is the only place that sends controls: it compares "chat has a
//     live child" with what was last sent for the run and sends the difference.
//     It runs when a child is spawned, when a run of the chat starts, and after
//     a child's execution is released (after the finish push, so the push is
//     injected into the still-held process before the hold goes away).
//   - Every exit path ends the hold: the last child finishing or being killed
//     releases it; stopping or crashing the parent ends the run, and the guard
//     release forgets the state (a new run starts with a fresh state); a
//     release that never gets through (backend restart, runner offline) is
//     bounded by aw's idle cap (holdIdleCap) and the run's own cancellation.
//   - A CLI that cannot hold (opencode, simplest) ignores the control; its
//     parent behaves as before and is restarted by pushes through the queue.
//
// While the run is idle-held (between turns, waiting for children) aw reports
// turn_idle / turn_active. The backend then shows the chat as not running
// (agent status completed, SSE status isRunning=false with waitingSubagents)
// and marks it waiting on its descendants in the dir lock so writable
// children can take the checkout. The mark is cleared BEFORE a message is
// injected for the next turn (prepareHeldInject); when it cannot be cleared
// because a writable child still holds the checkout, the message is not
// injected but queued, and runs when the hold ends. The guard of the chat
// stays held all along, so a release of the guard still clears the mark.

// defaultHoldIdle is how long a held run may sit idle (no turn, no message)
// before aw closes its input anyway. ASGARD_SUBAGENT_HOLD_IDLE overrides it.
const defaultHoldIdle = 30 * time.Minute

// holdIdleCap is the idle cap sent with a hold.
func (s *Server) holdIdleCap() time.Duration {
	if s.holdIdle > 0 {
		return s.holdIdle
	}
	if v := os.Getenv("ASGARD_SUBAGENT_HOLD_IDLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultHoldIdle
}

// runHolder is implemented by the injector (runnerhub.Hub) when it can hold
// runs.
type runHolder interface {
	ControlRun(nodeID, runID, action string, idle time.Duration) error
}

// holdState is what the backend knows about the held run of one chat.
type holdState struct {
	runID   string
	agentID string
	// holding: a hold was sent and not released since.
	holding bool
	// idle: the run waits for input between turns; the chat is marked in the
	// dir lock with reg.
	idle bool
	reg  *waitReg
	// lastGen is the idle generation of the last turn report applied. aw
	// numbers its idle changes (common.MetaIdleGen) and reports them in order;
	// a report that is not newer than lastGen is stale (an idle from before a
	// message was injected, or a duplicate) and is dropped. That replaces any
	// guess by clock: the idle state this chat went through before an
	// injection has its generation applied already, and whatever the injected
	// turn reports is newer.
	lastGen uint64
	// inflight counts injections being delivered; succ counts those delivered
	// since the run went idle.
	inflight, succ int
	// pending is an idle report that arrived while an injection was in
	// flight. The pre-inject idle it may be is not applied (it would mark a
	// turn that is starting and could let a writable child into the
	// checkout); done decides: applied if the injection failed or the report
	// is newer than the injected turn's start, dropped otherwise.
	pending *pendingIdle
	// fromIdle: an injection in flight was prepared from the idle state (its
	// mark cleared). Reset when the last injection in flight is done.
	fromIdle bool
}

// pendingIdle is a stashed idle report (see holdState.pending).
type pendingIdle struct {
	gen    uint64
	hasGen bool
}

// holdBook is the hold states of all chats.
type holdBook struct {
	mu sync.Mutex
	m  map[string]*holdState
}

// currentRun returns the run of the chat's current execution.
func (s *Server) currentRun(chatID string) (nodeID, runID, agentID string, ok bool) {
	if s.activeRuns == nil {
		return "", "", "", false
	}
	v, running := s.activeExecutions.Load(chatID)
	if !running {
		return "", "", "", false
	}
	h, _ := v.(*executionHandle)
	if h == nil || h.isWorkflow || h.agentID == "" {
		return "", "", "", false
	}
	nodeID, runID, ok = s.activeRuns.Lookup(chatID, h.agentID)
	return nodeID, runID, h.agentID, ok
}

// hasLiveChildren reports whether a direct child of the chat runs, waits for
// the checkout or is about to run a queued message.
func (s *Server) hasLiveChildren(chatID string) bool {
	if s.repo == nil {
		return false
	}
	ids, err := s.repo.ChildChatIDs(chatID)
	if err != nil {
		// Unknown: hold, the idle cap bounds a hold nobody releases.
		return true
	}
	for _, id := range ids {
		if !s.childFinished(id) {
			return true
		}
	}
	return false
}

// holdStateLocked returns the state for the chat's current run, replacing the
// state of a run that is gone (clearing its mark).
func (s *Server) holdStateLocked(chatID, runID, agentID string) *holdState {
	b := &s.holds
	if b.m == nil {
		b.m = map[string]*holdState{}
	}
	st := b.m[chatID]
	if st != nil && st.runID != runID {
		s.dropHoldLocked(chatID, st)
		st = nil
	}
	if st == nil {
		st = &holdState{runID: runID, agentID: agentID, reg: &waitReg{chat: chatID}}
		b.m[chatID] = st
	}
	return st
}

// dropHoldLocked forgets a hold state and the mark it kept.
func (s *Server) dropHoldLocked(chatID string, st *holdState) {
	if st.idle {
		s.releaseWaitMark(chatID, st.reg)
	}
	delete(s.holds.m, chatID)
}

// syncHold makes the hold of the chat's run match whether it has live
// children (see the file comment).
func (s *Server) syncHold(chatID string) {
	hr, ok := s.injector.(runHolder)
	if !ok || s.activeRuns == nil {
		return
	}
	b := &s.holds
	b.mu.Lock()
	defer b.mu.Unlock()
	nodeID, runID, agentID, running := s.currentRun(chatID)
	if !running {
		if st := b.m[chatID]; st != nil {
			s.dropHoldLocked(chatID, st)
		}
		return
	}
	st := s.holdStateLocked(chatID, runID, agentID)
	live := s.hasLiveChildren(chatID)
	if live == st.holding {
		return
	}
	action, idle := runnerhub.RunControlRelease, time.Duration(0)
	if live {
		action, idle = runnerhub.RunControlHold, s.holdIdleCap()
	}
	if err := hr.ControlRun(nodeID, runID, action, idle); err != nil {
		if !errors.Is(err, runnerhub.ErrUnknownRun) {
			log.Warn().Err(err).Str("chat_id", chatID).Str("action", action).Msg("failed to send run control")
		}
		return
	}
	st.holding = live
	if !live && st.idle {
		// aw closes the idle process now; the chat no longer waits for
		// children.
		st.idle = false
		s.releaseWaitMark(chatID, st.reg)
	}
}

// forgetHold drops the hold state of a chat whose execution ended.
func (s *Server) forgetHold(chatID string) {
	b := &s.holds
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.m[chatID]; st != nil {
		if _, running := s.activeExecutions.Load(chatID); running {
			return // a newer execution; syncHold replaces the state if its run differs
		}
		s.dropHoldLocked(chatID, st)
	}
}

// isIdleHeld reports whether the chat's run waits between turns for its
// subagents.
func (s *Server) isIdleHeld(chatID string) bool {
	b := &s.holds
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[chatID]
	return st != nil && st.idle
}

// onTurnReport handles aw's turn_idle / turn_active report of a held run. gen
// is the idle generation it carries (hasGen false: none, always applied).
//
// Reports ride the lossy status fan-out, so one may be missing. A missing
// turn_active is harmless (the next report supersedes it); a missing
// turn_idle leaves the chat looking busy until aw repeats its state (it does
// on every hold control) or the next turn ends, and a message for it is
// queued meanwhile. A turn_active always clears an idle mark at once, so an
// idle applied from before an injection never outlives the injected turn's
// first report.
func (s *Server) onTurnReport(chatID, entryType string, gen uint64, hasGen bool) {
	idle := entryType == common.EntryTypeTurnIdle
	b := &s.holds
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[chatID]
	if st == nil || !st.holding && idle {
		return
	}
	if _, runID, _, ok := s.currentRun(chatID); !ok || runID != st.runID {
		return
	}
	if hasGen && gen <= st.lastGen {
		return
	}
	if idle && st.inflight > 0 {
		// Possibly from before the message being injected: decide in done.
		if st.pending == nil || hasGen && gen > st.pending.gen {
			st.pending = &pendingIdle{gen: gen, hasGen: hasGen}
		}
		return
	}
	if hasGen {
		st.lastGen = gen
	}
	if !idle && st.pending != nil && hasGen && gen > st.pending.gen {
		st.pending = nil // superseded
	}
	if idle {
		s.markIdleLocked(chatID, st)
		return
	}
	if !st.idle {
		return
	}
	st.idle = false
	s.releaseWaitMark(chatID, st.reg)
	s.setHeldStatus(chatID, st.agentID, false)
}

// markIdleLocked shows the chat as waiting between turns and marks it in the
// dir lock.
func (s *Server) markIdleLocked(chatID string, st *holdState) {
	if st.idle {
		return
	}
	st.idle, st.succ = true, 0
	s.holdWaitMark(chatID, st.reg)
	s.setHeldStatus(chatID, st.agentID, true)
}

// turnReportGen extracts the idle generation from the metadata of a turn
// report.
func turnReportGen(meta map[string]any) (uint64, bool) {
	switch v := meta[common.MetaIdleGen].(type) {
	case float64:
		return uint64(v), v >= 0
	case int:
		return uint64(v), v >= 0
	case int64:
		return uint64(v), v >= 0
	case uint64:
		return v, true
	}
	return 0, false
}

// setHeldStatus shows the chat as between turns (waiting) or running a turn.
func (s *Server) setHeldStatus(chatID, agentID string, waiting bool) {
	status := dbmodels.AgentStatusRunning
	if waiting {
		status = dbmodels.AgentStatusCompleted
	}
	if s.repo != nil {
		if err := s.repo.UpdateAgentStatus(chatID, agentID, status); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update the status of a held chat")
		}
	}
	payload := map[string]any{"agent": agentID, "isRunning": !waiting}
	if waiting {
		payload["waitingSubagents"] = true
	}
	s.PublishSessionEvent(chatID, SessionEvent{Type: "status", Payload: payload})
}

// prepareHeldInject readies an idle-held chat for a message to be injected:
// its waiting mark must be cleared before the next turn writes. False means
// the mark cannot be cleared (a writable child holds the checkout): do not
// inject, queue the message. done must be called with the outcome and, when
// injected, the idle generation aw replied with (0: unknown); reports up to it
// predate the message and become stale.
func (s *Server) prepareHeldInject(chatID string) (ok bool, done func(injected bool, replyGen uint64)) {
	b := &s.holds
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[chatID]
	if st == nil {
		return true, func(bool, uint64) {}
	}
	if st.idle {
		s.releaseWaitMark(chatID, st.reg)
		if s.chatMarked(chatID) {
			// Still marked: a writable descendant holds the checkout.
			s.holdWaitMark(chatID, st.reg)
			return false, nil
		}
		// The idle generation st.lastGen is the one this injection ends;
		// reports up to it are stale from here on.
		st.idle, st.succ = false, 0
		st.fromIdle = true
		s.setHeldStatus(chatID, st.agentID, false)
	}
	st.inflight++
	return true, func(injected bool, replyGen uint64) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.m[chatID] != st {
			return
		}
		st.inflight--
		p := st.pending
		if injected {
			st.succ++
			if replyGen > st.lastGen {
				st.lastGen = replyGen
			}
			// An idle report newer than the injected turn's start is that
			// turn's own end; anything older (or any, when aw gave no
			// generation) is the pre-inject idle.
			if p != nil && p.hasGen && replyGen > 0 && p.gen > replyGen {
				st.pending = nil
				st.lastGen = max(st.lastGen, p.gen)
				s.markIdleLocked(chatID, st)
			}
		}
		if st.inflight > 0 {
			return
		}
		st.pending = nil
		wasIdle := st.fromIdle
		st.fromIdle = false
		if st.succ > 0 || st.idle || !st.holding {
			return
		}
		if !wasIdle && p == nil {
			// Prepared mid-turn and no idle was reported: the turn is still
			// running, leave the state alone (marking would let a writable
			// child in beside it).
			return
		}
		// Nothing was injected and the process may still be idle: it will not
		// report that again, so take the idle state back.
		if p != nil && p.hasGen {
			st.lastGen = max(st.lastGen, p.gen)
		}
		s.markIdleLocked(chatID, st)
	}
}

// chatMarked reports whether the chat is still marked in the dir lock.
func (s *Server) chatMarked(chatID string) bool {
	g := &s.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	_, marked := g.marked[chatID]
	return marked
}
