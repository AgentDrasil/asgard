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
  chatInputText: Ref<string>,
  router: Router,
  messages: Ref<ChatMessage[]>,
) {
  const loading = ref(false);
  const isStreaming = ref(false);

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
    chatInputText.value = "";
    let currentThreadId = activeSessionId.value;

    isStreaming.value = true;
    loading.value = true;

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

          const agentName =
            (metadata?.["agent_name"] as string) || activeAgent.value?.name || "Agent";

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
              });
            }
            return;
          }

          if (!toolLog) {
            toolLog = statusText;
          } else {
            toolLog += `\n${TOOL_ITEM_DELIMITER}\n` + statusText;
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
            messages.value = messages.value.map((m) =>
              m.id === reasoningMsgId ? { ...m, content: toolLog } : m,
            );
          }
        },
        onError: async (err) => {
          messages.value.push({
            id: `error-${crypto.randomUUID()}`,
            role: "activity",
            activityType: "ERROR",
            content: err.message || "An execution error occurred.",
            timestamp: Date.now(),
          });
          isStreaming.value = false;
          loading.value = false;
        },
        onComplete: async () => {
          isStreaming.value = false;
          loading.value = false;
          if (currentThreadId && !currentSession.title) {
            await refreshSessionTitle(currentThreadId);
          }
          const updated = await getSessions();
          sessions.value = updated;
        },
      },
    );
  };

  return {
    messages,
    loading,
    isStreaming,
    handleSendMessage,
  };
}
