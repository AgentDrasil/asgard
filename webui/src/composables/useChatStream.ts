import { ref, type Ref } from "vue";
import type { Router } from "vue-router";
import type { AgentInfo, ChatSession, ChatMessage } from "../types";
import { createSession, getSession, getSessions } from "../lib/api";
import { runAgentStream } from "../lib/agent";
import { TOOL_ITEM_DELIMITER } from "../utils/messageUtils";

export function useChatStream(
  activeSessionId: Ref<string | null>,
  sessions: Ref<ChatSession[]>,
  agents: Ref<AgentInfo[]>,
  activeAgent: Ref<AgentInfo | null>,
  selectedAgentId: Ref<string>,
  selectedDir: Ref<string>,
  selectedModel: Ref<string>,
  chatInputText: Ref<string>,
  router: Router,
  messages: Ref<ChatMessage[]>,
) {
  const loading = ref(false);
  const isStreaming = ref(false);
  // Label of the sub-agent currently executing (workflow node events); null
  // falls back to the session's active agent name in the UI.
  const workingAgentLabel = ref<string | null>(null);

  const resolveAgentName = (metadata?: Record<string, any>): string | undefined => {
    const agentId = metadata?.["agent_id"] as string | undefined;
    if (agentId) {
      const matched = agents.value.find((a) => a.id === agentId);
      if (matched) return matched.name;
    }
    const agentName = metadata?.["agent_name"] as string | undefined;
    if (agentName) return agentName;
    return activeAgent.value?.name;
  };

  const pushErrorMessage = (content: string, agentName?: string) => {
    if (!content) return;
    const exists = messages.value.some((m) => m.role === "error" && m.content === content);
    if (exists) return;
    messages.value.push({
      id: `error-${crypto.randomUUID()}`,
      role: "error",
      content,
      agentName: agentName || activeAgent.value?.name,
      timestamp: Date.now(),
    });
  };

  const refreshSessionTitle = async (chatID: string) => {
    const sess = await getSession(chatID);
    if (sess && sess.title) {
      const idx = sessions.value.findIndex((s) => s.chatID === chatID);
      if (idx > -1) {
        sessions.value[idx] = { ...sessions.value[idx], title: sess.title };
      }
    }
  };

  const handleSendMessage = async (text: string) => {
    let currentThreadId = activeSessionId.value;

    if (currentThreadId) {
      const activeSess = sessions.value.find((s) => s.chatID === currentThreadId);
      if (activeSess?.isRunning || isStreaming.value || loading.value) {
        return;
      }
    }

    chatInputText.value = "";

    isStreaming.value = true;
    loading.value = true;
    workingAgentLabel.value = null;

    if (!currentThreadId) {
      const created = await createSession(selectedAgentId.value, selectedDir.value);
      if (created && created.chatID) {
        currentThreadId = created.chatID;
        activeSessionId.value = currentThreadId;
        sessions.value = [created, ...sessions.value.filter((s) => s.chatID !== created.chatID)];
        await router.push(`/chat/${currentThreadId}`);
      }
    }

    const currentSession = sessions.value.find((s) => s.chatID === currentThreadId) || {
      chatID: currentThreadId || "",
      currentAgent: selectedAgentId.value,
      runDir: selectedDir.value,
      title: "",
    };

    const userMsgId = `user-${crypto.randomUUID()}`;
    messages.value.push({
      id: userMsgId,
      role: "user",
      content: text,
      timestamp: Date.now(),
    });

    const runId = crypto.randomUUID();
    const assistantMsgId = crypto.randomUUID();
    const reasoningMsgId = `reasoning-${runId}`;

    let hasAssistantMsg = false;
    let hasReasoningMsg = false;
    let toolLog = "";

    if (!currentSession.title && currentThreadId) {
      setTimeout(() => refreshSessionTitle(currentThreadId!), 1500);
    }

    const matchedAgent = agents.value.find(
      (a) => a.id === currentSession.currentAgent || a.name === currentSession.currentAgent,
    );
    const targetAgentId = matchedAgent ? matchedAgent.id : currentSession.currentAgent;

    await runAgentStream(
      targetAgentId,
      {
        prompt: text,
        runDir: currentSession.runDir || selectedDir.value,
        threadId: currentThreadId || undefined,
        runId,
        userMsgId,
        model: selectedModel.value || undefined,
      },
      {
        onText: (textContent, inputTokens, maxTokens) => {
          if (!hasAssistantMsg) {
            hasAssistantMsg = true;
            messages.value.push({
              id: assistantMsgId,
              role: "assistant",
              content: textContent,
              timestamp: Date.now(),
              ...(inputTokens ? { inputTokens } : {}),
              ...(maxTokens ? { maxTokens } : {}),
            });
            if (!currentSession.title && currentThreadId) {
              refreshSessionTitle(currentThreadId);
            }
          } else {
            messages.value = messages.value.map((m) =>
              m.id === assistantMsgId
                ? {
                    ...m,
                    content: textContent,
                    ...(inputTokens ? { inputTokens } : {}),
                    ...(maxTokens ? { maxTokens } : {}),
                  }
                : m,
            );
          }
        },
        onStatus: (statusText, entryType, _state, metadata) => {
          if (!statusText) return;

          const agentName = resolveAgentName(metadata) || activeAgent.value?.name || "Agent";
          const targetFiles = (metadata?.["target_files"] as string[] | undefined) || undefined;
          const artifactFiles = (metadata?.["artifact_files"] as string[] | undefined) || undefined;

          // Track which sub-agent is currently executing (workflow node events)
          const nodeStatus = metadata?.["node_status"] as string | undefined;
          const nodeAgentId = metadata?.["agent_id"] as string | undefined;
          if (nodeStatus === "RUNNING" && nodeAgentId) {
            workingAgentLabel.value = resolveAgentName(metadata) || nodeAgentId;
          }

          if (entryType === "error") {
            pushErrorMessage(statusText, agentName);
            return;
          }

          if (entryType === "ask_user") {
            const askMsgId = (metadata?.["message_id"] as string) || `ask-${Date.now()}`;
            const exists = messages.value.some((m) => m.id === askMsgId);
            if (!exists) {
              messages.value.push({
                id: askMsgId,
                role: "ask_user",
                content: statusText,
                agentName: agentName,
                timestamp: Date.now(),
                ...(artifactFiles ? { artifactFiles } : {}),
              });
            }
            return;
          }

          if (!toolLog) {
            toolLog = statusText;
          } else {
            // Append incremental status updates with delimiter if not already present in toolLog
            if (!toolLog.endsWith(statusText)) {
              toolLog += `\n${TOOL_ITEM_DELIMITER}\n` + statusText;
            }
          }

          const exists = messages.value.some((m) => m.id === reasoningMsgId);
          if (!exists) {
            const bubble: ChatMessage = {
              id: reasoningMsgId,
              role: "tool_call",
              activityType: "TOOL",
              content: toolLog,
              agentName: agentName,
              timestamp: Date.now(),
              ...(targetFiles ? { targetFiles } : {}),
              ...(artifactFiles ? { artifactFiles } : {}),
            };
            if (hasAssistantMsg) {
              const assistantIdx = messages.value.findIndex((m) => m.id === assistantMsgId);
              if (assistantIdx > -1) {
                messages.value.splice(assistantIdx, 0, bubble);
              } else {
                messages.value.push(bubble);
              }
            } else {
              messages.value.push(bubble);
            }
            if (!hasReasoningMsg) hasReasoningMsg = true;
          } else {
            messages.value = messages.value.map((m) => {
              if (m.id !== reasoningMsgId) return m;
              const mergedTargetFiles = targetFiles
                ? Array.from(new Set([...(m.targetFiles || []), ...targetFiles]))
                : m.targetFiles;
              const mergedArtifactFiles = artifactFiles
                ? Array.from(new Set([...(m.artifactFiles || []), ...artifactFiles]))
                : m.artifactFiles;
              return {
                ...m,
                content: toolLog,
                ...(mergedTargetFiles ? { targetFiles: mergedTargetFiles } : {}),
                ...(mergedArtifactFiles ? { artifactFiles: mergedArtifactFiles } : {}),
              };
            });
          }
        },
        onError: async (err) => {
          pushErrorMessage(err.message || "An execution error occurred.");
          workingAgentLabel.value = null;
          isStreaming.value = false;
          loading.value = false;
          const updatedSessions = await getSessions();
          sessions.value = updatedSessions;
        },
        onComplete: async () => {
          isStreaming.value = false;
          loading.value = false;
          workingAgentLabel.value = null;
          if (currentThreadId && !currentSession.title) {
            await refreshSessionTitle(currentThreadId);
          }
          const updatedSessions = await getSessions();
          sessions.value = updatedSessions;
        },
      },
    );
  };

  return {
    messages,
    loading,
    isStreaming,
    workingAgentLabel,
    handleSendMessage,
  };
}
