package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// TriggerMessageRequest represents the payload for POST /api/agents/{id}/message.
type TriggerMessageRequest struct {
	Prompt   string `json:"prompt"`
	ChatID   string `json:"chatId,omitempty"`
	RunDir   string `json:"runDir,omitempty"`
	Model    string `json:"model,omitempty"`
	Wait     bool   `json:"wait,omitempty"`
	Headless bool   `json:"-"`

	Metadata map[string]any `json:"metadata,omitempty"`

	Attachments []dbmodels.Attachment `json:"attachments,omitempty"`
}

// formatPromptWithAttachments formats a user prompt with attached files info for sandboxed agent execution.
// It enforces zero-trust validation:
// 1. Limits attachments to at most 20 entries.
// 2. Ignores any client-supplied Path.
// 3. Sanitizes Name via filepath.Base, length <= 255, and control character filtering.
// 4. Generates sandbox path strictly as /tmp/attachments/<safeName>.
func formatPromptWithAttachments(prompt string, attachments []dbmodels.Attachment) string {
	if len(attachments) == 0 {
		return prompt
	}

	maxAtts := 20
	if len(attachments) > maxAtts {
		attachments = attachments[:maxAtts]
	}

	type validAtt struct {
		name        string
		sandboxPath string
		size        int64
	}

	valid := make([]validAtt, 0, len(attachments))
	for _, att := range attachments {
		rawName := strings.TrimSpace(att.Name)
		if rawName == "" {
			continue
		}
		// Replace backslashes first for cross-platform safety
		rawName = strings.ReplaceAll(rawName, "\\", "/")
		base := filepath.Base(rawName)
		if base == "." || base == ".." || base == "/" || base != rawName {
			continue
		}
		if len(base) > 255 {
			continue
		}

		var sb strings.Builder
		hasControl := false
		for _, r := range base {
			if r < 32 || r == 127 || r == '/' || r == '\\' {
				hasControl = true
				break
			}
			sb.WriteRune(r)
		}
		if hasControl {
			continue
		}
		safeName := strings.TrimSpace(sb.String())
		if safeName == "" || safeName == "." || safeName == ".." {
			continue
		}

		sandboxPath := "/tmp/attachments/" + safeName
		valid = append(valid, validAtt{
			name:        safeName,
			sandboxPath: sandboxPath,
			size:        att.Size,
		})
	}

	if len(valid) == 0 {
		return prompt
	}

	var sb strings.Builder
	sb.WriteString(prompt)
	sb.WriteString("\n\n[Attached Files]\n")
	for _, att := range valid {
		fmt.Fprintf(&sb, "- %s (%s, %d bytes)\n", att.name, att.sandboxPath, att.size)
	}
	sb.WriteString("Please inspect and process these attachments directly from the sandbox filesystem.")

	return sb.String()
}

// handleTriggerMessage handles POST /api/agents/{id}/message, launching agent execution
// either asynchronously (202 Accepted) or synchronously (200 OK when wait=true).
func (s *Server) handleTriggerMessage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, `{"error":"agent id is required"}`, http.StatusBadRequest)
		return
	}

	var targetAgent *agentspec.Agent
	s.mu.RLock()
	for _, a := range s.agents {
		if a.Config.ID == agentID || a.Config.Name == agentID {
			targetAgent = a
			break
		}
	}
	s.mu.RUnlock()

	if targetAgent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent not found"})
		return
	}

	var req TriggerMessageRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
	}

	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
		return
	}

	chatID := req.ChatID
	if chatID == "" {
		chatID = uuid.NewV7().String()
	} else if !IsValidChatID(chatID) {
		http.Error(w, `{"error":"invalid chatId format"}`, http.StatusBadRequest)
		return
	}

	wait := req.Wait || r.URL.Query().Get("wait") == "true"

	// 1. Workflow 分支：完全遵循既有路径，获取 guard 失败返回 409，成功则执行 runWorkflow 并保留 defer Delete
	if targetAgent.Config.Type == "workflow" {
		if _, loaded := s.activeExecutions.LoadOrStore(chatID, struct{}{}); loaded {
			http.Error(w, `{"error":"session is already running a task"}`, http.StatusConflict)
			return
		}

		runDirOpt := optional.None[string]()
		if req.RunDir != "" {
			runDirOpt = optional.Some(req.RunDir)
		}
		if s.repo != nil {
			if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
			}
		}

		if wait {
			defer s.activeExecutions.Delete(chatID)
			status, output, err := s.runWorkflow(s.Context(), targetAgent, chatID, req)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": status,
					"error":  err.Error(),
					"chatId": chatID,
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": status,
				"output": output,
				"chatId": chatID,
			})
			return
		}

		// Async workflow
		go func() {
			defer s.activeExecutions.Delete(chatID)
			_, _, err := s.runWorkflow(s.Context(), targetAgent, chatID, req)
			if err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Str("agent", targetAgent.Config.ID).Msg("async workflow execution error")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "accepted",
			"chatId": chatID,
		})
		return
	}

	// 尝试获取 guard
	_, loaded := s.activeExecutions.LoadOrStore(chatID, struct{}{})

	// 2. 同步 Wait 运行中分支：维持既有契约返回 409 Conflict
	if wait && loaded {
		http.Error(w, `{"error":"session is already running a task"}`, http.StatusConflict)
		return
	}

	// 3. 同步 Wait 空闲分支：维持既有同步执行路径并保留 defer Delete
	if wait && !loaded {
		defer s.activeExecutions.Delete(chatID)
		runDirOpt := optional.None[string]()
		if req.RunDir != "" {
			runDirOpt = optional.Some(req.RunDir)
		}
		if s.repo != nil {
			if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
			}
		}
		status, output, err := s.runSingleAgent(s.Context(), targetAgent, chatID, req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": status,
				"error":  err.Error(),
				"chatId": chatID,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status,
			"output": output,
			"chatId": chatID,
		})
		return
	}

	// 4. 异步单 Agent 运行中入队分支：!wait && loaded
	if loaded {
		if len(req.Attachments) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "queued messages only support plain text; attachments are not allowed"})
			return
		}

		if s.repo != nil {
			existing, err := s.repo.GetQueuedMessages(chatID)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to check queue capacity: " + err.Error()})
				return
			}
			if len(existing) >= dbmodels.MaxQueuedMessagesPerSession {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "Queue limit reached (maximum 3 messages)"})
				return
			}

			qmsg, err := s.repo.EnqueueMessage(chatID, req.Prompt, req.Model)
			if err != nil {
				if errors.Is(err, dbmodels.ErrQueueFull) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Queue limit reached (maximum 3 messages)"})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to enqueue message: " + err.Error()})
				return
			}

			updatedQueue, _ := s.repo.GetQueuedMessages(chatID)
			if updatedQueue == nil {
				updatedQueue = []dbmodels.QueuedMessage{}
			}
			s.PublishSessionEvent(chatID, SessionEvent{
				Type:    EventTypeQueue,
				Payload: map[string]any{"queue": updatedQueue},
			})

			// 尝试启动共享消费（如果前序刚好完成）
			session, _ := s.repo.GetSession(chatID)
			runDir := req.RunDir
			if session != nil && session.RunDir != "" {
				runDir = session.RunDir
			}
			s.startQueueConsumerIfIdle(chatID, targetAgent, runDir)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":    "queued",
				"chatId":    chatID,
				"messageId": qmsg.ID,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "database unavailable in degraded mode"})
		return
	}

	// 5. 异步单 Agent 空闲首发分支：!wait && !loaded
	// 注意：移除原有的 defer Delete，将 guard 生命周期完全移交给 runSingleAgentWithQueue 管理
	runDirOpt := optional.None[string]()
	if req.RunDir != "" {
		runDirOpt = optional.Some(req.RunDir)
	}
	if s.repo != nil {
		if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
		}
	}

	go s.runSingleAgentWithQueue(s.Context(), targetAgent, chatID, req)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "accepted",
		"chatId": chatID,
	})
}

// startQueueConsumerIfIdle attempts to atomically acquire the guard and start consumer loop
func (s *Server) startQueueConsumerIfIdle(chatID string, targetAgent *agentspec.Agent, runDir string) bool {
	if _, loaded := s.activeExecutions.LoadOrStore(chatID, struct{}{}); loaded {
		return false
	}
	go s.runQueueConsumerLoop(s.Context(), targetAgent, chatID, runDir)
	return true
}

func (s *Server) executeSingleAgent(ctx context.Context, agent *agentspec.Agent, chatID string, req TriggerMessageRequest) (string, string, error) {
	if s.runSingleAgentFn != nil {
		return s.runSingleAgentFn(ctx, agent, chatID, req)
	}
	return s.runSingleAgent(ctx, agent, chatID, req)
}

// handleQueuedTaskError handles task execution errors for both the initial run and queued tasks.
// If the error is an agentRunError (sandbox error), subsequent queued messages are cleared and an error message is appended and published.
// If it is a non-sandbox failure (excluding cancellation/shutdown), a visible error message is appended and queued messages are preserved.
func (s *Server) handleQueuedTaskError(chatID string, targetAgent *agentspec.Agent, qErr error) {
	if s.repo == nil {
		return
	}
	var runErr *agentRunError
	if errors.As(qErr, &runErr) {
		// 核心沙箱失败：清空后续队列并追加错误通知
		_, _ = s.repo.ClearQueuedMessages(chatID)
		errMsg := dbmodels.ChatMessage{
			ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
			Role:      "error",
			Content:   fmt.Sprintf("任务执行失败：%v。已自动清空该会话所有排队消息。", qErr),
			AgentName: targetAgent.Config.Name,
			Timestamp: time.Now().UnixMilli(),
		}
		_ = s.repo.AppendMessage(chatID, errMsg)
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": []dbmodels.QueuedMessage{}}})
	} else if !errors.Is(qErr, context.Canceled) && !errors.Is(qErr, context.DeadlineExceeded) {
		// N10: 非沙箱类前置失败且非优雅停机：追加可见错误提示告知用户该消息未执行，保留剩余队列
		errMsg := dbmodels.ChatMessage{
			ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
			Role:      "error",
			Content:   fmt.Sprintf("任务调度失败（该消息未执行）：%v", qErr),
			AgentName: targetAgent.Config.Name,
			Timestamp: time.Now().UnixMilli(),
		}
		_ = s.repo.AppendMessage(chatID, errMsg)
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
	}
}

func (s *Server) runSingleAgentWithQueue(ctx context.Context, targetAgent *agentspec.Agent, chatID string, req TriggerMessageRequest) {
	// 第一阶段：执行首发任务
	_, _, qErr := s.executeSingleAgent(ctx, targetAgent, chatID, req)
	if qErr != nil {
		s.handleQueuedTaskError(chatID, targetAgent, qErr)
		s.activeExecutions.Delete(chatID)
		return
	}

	// 第二阶段：首发成功，进入单一消费循环执行后续排队任务
	s.runQueueConsumerLoop(ctx, targetAgent, chatID, req.RunDir)
}

func (s *Server) runQueueConsumerLoop(ctx context.Context, targetAgent *agentspec.Agent, chatID string, runDir string) {
	for {
		if s.repo == nil {
			s.activeExecutions.Delete(chatID)
			break
		}

		// 关键不变式：循环顶的 Pop 恒处于持 guard 临界区内
		nextMsg, popErr := s.repo.PopNextQueuedMessage(chatID)
		if popErr != nil {
			log.Error().Err(popErr).Msg("failed to pop next queued message")
			// N6: 追加可见错误消息但保留剩余队列，释放 guard 退出
			errMsg := dbmodels.ChatMessage{
				ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
				Role:      "error",
				Content:   fmt.Sprintf("获取排队消息失败：%v，会话已暂停。排队消息已保留，可重试或重启后继续。", popErr),
				AgentName: targetAgent.Config.Name,
				Timestamp: time.Now().UnixMilli(),
			}
			_ = s.repo.AppendMessage(chatID, errMsg)
			s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
			s.activeExecutions.Delete(chatID)
			break
		}
		if nextMsg == nil {
			// N1/N7: 释放 guard 并进行非破坏性 Peek 复检
			s.activeExecutions.Delete(chatID)
			head, perr := s.repo.PeekNextQueuedMessage(chatID)
			if perr != nil {
				log.Error().Err(perr).Msg("failed to peek next queued message")
				errMsg := dbmodels.ChatMessage{
					ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
					Role:      "error",
					Content:   fmt.Sprintf("获取排队消息失败：%v，会话已暂停。排队消息已保留，可重试或重启后继续。", perr),
					AgentName: targetAgent.Config.Name,
					Timestamp: time.Now().UnixMilli(),
				}
				_ = s.repo.AppendMessage(chatID, errMsg)
				s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
				break
			}
			if head == nil {
				break // 队列确已清空，安全退出
			}
			if _, loaded := s.activeExecutions.LoadOrStore(chatID, struct{}{}); loaded {
				break // 并发入队者已抢先获取 guard 并启动新消费协程；head 未被物理删除，由其承接，零消息丢失
			}
			// 成功重持 guard，此时执行破坏性 Pop 安全可靠
			nextMsg, _ = s.repo.PopNextQueuedMessage(chatID)
			if nextMsg == nil {
				// N9 修复：队头恰被用户撤回。本协程仍持有 guard，严禁在此 Delete！
				// 直接 continue 回到循环顶，保持“循环顶 Pop 恒持 guard”不变式，杜绝并发双跑与误删他人 guard
				continue
			}
		}

		// 广播出队后的最新队列快照
		remaining, _ := s.repo.GetQueuedMessages(chatID)
		if remaining == nil {
			remaining = []dbmodels.QueuedMessage{}
		}
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": remaining}})

		// 执行排队任务
		_, _, qErr := s.executeSingleAgent(ctx, targetAgent, chatID, TriggerMessageRequest{Prompt: nextMsg.Prompt, Model: nextMsg.Model, RunDir: runDir})
		if qErr != nil {
			s.handleQueuedTaskError(chatID, targetAgent, qErr)
			s.activeExecutions.Delete(chatID)
			break
		}
	}
}

// runSingleAgent executes a single CLI agent synchronously, returning its final assistant text.
func (s *Server) runSingleAgent(ctx context.Context, agent *agentspec.Agent, chatID string, req TriggerMessageRequest) (status string, output string, err error) {
	exec := NewSingleAgentExecutor(agent, s.conf, s.repo, s, nil)
	out, err := exec.Execute(ctx, SingleAgentRunParams{
		ChatID:      chatID,
		Prompt:      req.Prompt,
		RunDir:      req.RunDir,
		Model:       req.Model,
		Metadata:    req.Metadata,
		Attachments: req.Attachments,
	})
	if err != nil {
		return "failed", "", err
	}
	return "completed", out, nil
}
