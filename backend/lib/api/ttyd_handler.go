package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// ttyd WebSocket protocol command bytes (the browser terminal speaks ttyd's
// protocol; the shell itself runs on the runner behind RunnerService.Ttyd).
const (
	ttydInput  = '0' // client -> server
	ttydResize = '1' // client -> server
	ttydOutput = '0' // server -> client
)

var ttydUpgrader = websocket.Upgrader{
	Subprotocols:    []string{"tty"},
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
}

// ttydTarget is where a terminal session runs.
type ttydTarget struct {
	runner     string
	chatID     string
	workingDir *runnerpb.DirRef
}

// resolveTTYDTarget maps a session key onto the runner, chat and start
// directory: "agent-<chat>" opens in the chat's run dir on its runner, every
// other key is a plain shell on the runner named by the request (or the only
// known one).
func (s *Server) resolveTTYDTarget(r *http.Request, sessionKey string) (ttydTarget, int, string) {
	t := ttydTarget{}
	chatID, isAgent := strings.CutPrefix(sessionKey, "agent-")
	if isAgent && s.repo != nil {
		if !IsValidChatID(chatID) {
			return t, http.StatusBadRequest, "invalid session_id format"
		}
		sess, err := s.repo.GetSession(chatID)
		if err != nil {
			log.Error().Err(err).Str("agent_session_id", chatID).Msg("failed to query agent session for ttyd working directory")
			return t, http.StatusInternalServerError, "failed to query session"
		}
		if sess == nil {
			return t, http.StatusNotFound, "session not found"
		}
		t.chatID, t.runner = chatID, sess.RunnerNodeID
		if sess.RunDir != "" {
			runDir := NormalizeSessionRunDir(sess.RunDir, chatID)
			if code, msg := s.checkWorkspaceRunDir(chatID, runDir); code != 0 {
				return t, code, msg
			}
			t.workingDir = agents.DirRefFor(chatID, runDir)
		}
		if t.runner == "" && s.bindingEnforced() {
			return t, http.StatusBadRequest, "chat is not bound to a runner"
		}
	}
	if t.runner == "" {
		node, err := s.requestRunner(r)
		if err != nil {
			status, msg := fsErrorStatus(err)
			return t, status, msg
		}
		t.runner = node
	}
	return t, 0, ""
}

// handleTTYD serves the terminal WebSocket at /api/ttyd/{session_key}/ws. The
// shell runs on the runner; frames are relayed between the browser's ttyd
// protocol and RunnerService.Ttyd.
func (s *Server) handleTTYD(w http.ResponseWriter, r *http.Request) {
	if s.runnerClient == nil {
		writeJSONError(w, http.StatusInternalServerError, "ttyd requires a runner")
		return
	}

	rawPath := r.PathValue("session_id")
	if rawPath == "" {
		// Fallback for path stripping / wildcard routing
		rawPath = strings.TrimPrefix(r.URL.Path, "/api/ttyd/")
	}
	sessionKey, _, _ := strings.Cut(rawPath, "/")
	if sessionKey == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		writeJSONError(w, http.StatusBadRequest, "websocket upgrade required")
		return
	}

	target, status, msg := s.resolveTTYDTarget(r, sessionKey)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	client, err := s.nodeClient(target.runner)
	if err != nil {
		writeFSError(w, err)
		return
	}

	conn, err := ttydUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already answered
	}
	defer func() { _ = conn.Close() }()

	// ttyd's first client message is the JSON that spawns the shell.
	_, first, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var init struct {
		Columns uint32 `json:"columns"`
		Rows    uint32 `json:"rows"`
	}
	_ = json.Unmarshal(first, &init)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	st, err := client.Ttyd(ctx)
	if err != nil {
		closeTTYD(conn, websocket.CloseInternalServerErr, "failed to start ttyd session")
		return
	}
	if err := st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Open{Open: &runnerpb.TtydOpen{
		SessionKey: sessionKey, WorkingDir: target.workingDir, Cols: init.Columns, Rows: init.Rows, ChatId: target.chatID,
	}}}); err != nil {
		closeTTYD(conn, websocket.CloseInternalServerErr, "failed to start ttyd session")
		return
	}

	// Browser -> runner. A WebSocket close ends the shell (single-client).
	go func() {
		defer cancel()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) == 0 {
				continue
			}
			var f *runnerpb.TtydFrame
			switch msg[0] {
			case ttydInput:
				f = &runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Input{Input: msg[1:]}}
			case ttydResize:
				var sz struct {
					Columns uint32 `json:"columns"`
					Rows    uint32 `json:"rows"`
				}
				if json.Unmarshal(msg[1:], &sz) != nil {
					continue
				}
				f = &runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Resize{Resize: &runnerpb.TtydResize{Cols: sz.Columns, Rows: sz.Rows}}}
			default:
				continue // pause/resume and unknown commands
			}
			if st.Send(f) != nil {
				return
			}
		}
	}()

	// Runner -> browser.
	for {
		f, err := st.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				closeTTYD(conn, websocket.CloseGoingAway, "runner connection lost")
			}
			return
		}
		switch p := f.GetPayload().(type) {
		case *runnerpb.TtydFrame_Output:
			if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{ttydOutput}, p.Output...)); err != nil {
				return
			}
		case *runnerpb.TtydFrame_Exit:
			closeTTYD(conn, websocket.CloseNormalClosure, "")
			return
		}
	}
}

func closeTTYD(conn *websocket.Conn, code int, text string) {
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
}
