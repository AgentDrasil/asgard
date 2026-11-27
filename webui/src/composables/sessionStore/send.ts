import { ref, computed } from "vue";
import type { Router } from "vue-router";
import type { ChatSession, ChatMessage, Attachment } from "../../types";
import {
  createSession,
  triggerAgentMessage,
  uploadAttachment,
  stopSessionExecution,
  deleteSessionFromLocal,
  RunnerRequestError,
} from "../../lib/api";
import { t } from "../../i18n";
import { useToast } from "../useToast";
import { MAX_QUEUED_MESSAGES, type SessionState } from "./state";
import type { createConnection } from "./connection";
import { enqueueMessage } from "../../lib/api";

/** Sending a message (creating the chat on first send) and stopping a run. */
export function createSender(
  state: SessionState,
  connection: ReturnType<typeof createConnection>,
  closeSession: () => void,
  router?: Router,
) {
  const {
    agents,
    sessions,
    activeSessionId,
    activeSession,
    rawMessages,
    upsertMessage,
    replaceMessage,
    queuedMessages,
    isRunning,
    waitingSubagents,
    loading,
    workingAgentLabel,
    isInputBusy,
    gens,
    pushErrorMessage,
  } = state;
  const { events } = connection;

  // stopExecution aborts the in-flight task of a session (defaults to the
  // active session). The backend broadcasts queue/status/done over SSE; the
  // local state is reset optimistically so the input unlocks immediately.
  // In-flight stop targets: the guard is per chat so killing one subagent
  // never blocks (or fails) a stop of another chat.
  const stoppingTargets = ref<Set<string>>(new Set());
  const isStoppingSession = computed(
    () => !!activeSessionId.value && stoppingTargets.value.has(activeSessionId.value),
  );
  const stopExecution = async (sessionId?: string): Promise<boolean> => {
    const targetId = sessionId || activeSessionId.value;
    if (!targetId || stoppingTargets.value.has(targetId)) return false;
    stoppingTargets.value = new Set(stoppingTargets.value).add(targetId);
    try {
      const ok = await stopSessionExecution(targetId);
      if (ok) {
        // Stopping another chat (a subagent) leaves the open chat alone.
        if (targetId === activeSessionId.value) {
          queuedMessages.value = [];
          isRunning.value = false;
          loading.value = false;
          workingAgentLabel.value = null;
          if (activeSession.value) {
            activeSession.value.isRunning = false;
          }
        }
        const idx = sessions.value.findIndex((s) => s.chatID === targetId);
        if (idx > -1) {
          sessions.value[idx] = { ...sessions.value[idx], isRunning: false };
        }
      } else {
        useToast().error(t("chat.stopExecutionFailed"));
      }
      return ok;
    } finally {
      const next = new Set(stoppingTargets.value);
      next.delete(targetId);
      stoppingTargets.value = next;
    }
  };

  const sendMessage = async (
    text: string,
    opts?: {
      selectedAgentId?: string;
      selectedDir?: string;
      selectedModel?: string;
      attachments?: Attachment[];
      pendingFiles?: File[];
      allowCrossSession?: boolean;
      runnerId?: string;
      worktree?: boolean;
    },
  ) => {
    let currentThreadId = activeSessionId.value;

    if (currentThreadId && isStoppingSession.value) {
      return;
    }

    if (currentThreadId && (isRunning.value || queuedMessages.value.length > 0)) {
      if (
        (opts?.attachments && opts.attachments.length > 0) ||
        (opts?.pendingFiles && opts.pendingFiles.length > 0)
      ) {
        pushErrorMessage(t("chat.queueTextOnly"));
        return;
      }
      if (queuedMessages.value.length >= MAX_QUEUED_MESSAGES) {
        pushErrorMessage(t("chat.queueLimitPlaceholder", { max: MAX_QUEUED_MESSAGES }));
        return;
      }
      const queued = await enqueueMessage(currentThreadId, text);
      if (queued) {
        if (!queuedMessages.value.some((m) => m.id === queued.id)) {
          queuedMessages.value = [...queuedMessages.value, queued];
        }
      } else {
        pushErrorMessage(t("chat.queueEnqueueFailed"));
      }
      return;
    }

    if (currentThreadId) {
      const activeSess = sessions.value.find((s) => s.chatID === currentThreadId);
      if (activeSess?.isRunning || isInputBusy.value || isStoppingSession.value) {
        return;
      }
    }

    loading.value = true;
    gens.runState++;
    isRunning.value = true;
    waitingSubagents.value = false;
    workingAgentLabel.value = null;

    const userMsgId = `user-${crypto.randomUUID()}`;
    const userMsg: ChatMessage = {
      id: userMsgId,
      role: "user",
      content: text,
      timestamp: Date.now(),
      attachments: opts?.attachments,
    };
    upsertMessage(userMsg, () => userMsg);

    let createdHere = false;
    if (!currentThreadId) {
      const targetAgent = opts?.selectedAgentId || "";
      const targetDir = opts?.selectedDir || "";
      let created: ChatSession | null = null;
      let runnerFailure = "";
      try {
        if (opts?.runnerId) {
          // worktree is only passed when chosen, like the other opt-ins.
          created = await createSession(
            targetAgent,
            targetDir,
            opts.allowCrossSession,
            opts.runnerId,
            ...(opts.worktree ? [true] : []),
          );
        } else if (opts?.allowCrossSession !== undefined) {
          created = await createSession(targetAgent, targetDir, opts.allowCrossSession);
        } else {
          created = await createSession(targetAgent, targetDir);
        }
      } catch (err) {
        if (err instanceof RunnerRequestError) {
          runnerFailure =
            err.status === 503 ? t("runners.offlineError") : t("runners.conflictError");
        } else {
          throw err;
        }
      }
      if (created && created.chatID) {
        currentThreadId = created.chatID;
        createdHere = true;
        activeSessionId.value = currentThreadId;
        activeSession.value = created;
        sessions.value = [created, ...sessions.value.filter((s) => s.chatID !== created.chatID)];
        events.connect(currentThreadId);
        if (router) {
          await router.push(`/chat/${currentThreadId}`);
        }

        if (opts?.pendingFiles && opts.pendingFiles.length > 0) {
          const uploadedAttachments: Attachment[] = [];
          for (const file of opts.pendingFiles) {
            try {
              const att = await uploadAttachment(currentThreadId, file);
              uploadedAttachments.push(att);
            } catch (err: any) {
              console.error(`Failed to upload attachment ${file.name}:`, err);
              pushErrorMessage(
                t("chat.failedToUpload", { name: file.name, error: err?.message || err }),
              );
            }
          }
          const combined = [...(opts?.attachments || []), ...uploadedAttachments];
          const finalAttachments = combined.length > 0 ? combined : undefined;
          userMsg.attachments = finalAttachments;
          replaceMessage(userMsgId, { ...userMsg });
        }
      } else {
        rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
        loading.value = false;
        isRunning.value = false;
        pushErrorMessage(runnerFailure || t("chat.createSessionFailed"));
        return;
      }
    }

    const foundSession =
      (activeSession.value?.chatID === currentThreadId ? activeSession.value : null) ||
      sessions.value.find((s) => s.chatID === currentThreadId);

    const currentSession = foundSession || {
      chatID: currentThreadId || "",
      currentAgent: opts?.selectedAgentId || "",
      runDir: opts?.selectedDir || "",
      title: "",
    };

    const matchedAgent = agents.value.find(
      (a) => a.id === currentSession.currentAgent || a.name === currentSession.currentAgent,
    );
    const targetAgentId = matchedAgent ? matchedAgent.id : currentSession.currentAgent;

    const effectiveAllowCrossSession = opts?.allowCrossSession ?? currentSession.allowCrossSession;
    const triggerParams: any = {
      prompt: text,
      chatId: currentThreadId,
      runDir: currentSession.runDir || opts?.selectedDir,
      model: opts?.selectedModel,
      metadata: { message_id: userMsgId },
      attachments: userMsg.attachments,
    };
    if (effectiveAllowCrossSession !== undefined) {
      triggerParams.allowCrossSession = effectiveAllowCrossSession;
    }
    const res = await triggerAgentMessage(targetAgentId, triggerParams);

    // The trigger was in flight: the user may have moved to another chat since.
    // The loading/running state and error bubbles belong to the active chat, so
    // only touch them when it is still the chat this request was for.
    const stillActive = activeSessionId.value === currentThreadId;
    const resetBusy = () => {
      if (!stillActive) return;
      loading.value = false;
      isRunning.value = false;
    };
    const pushErrorIfActive = (content: string) => {
      if (stillActive) pushErrorMessage(content);
    };

    if (res?.runnerOffline) {
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
      resetBusy();
      if (createdHere && currentThreadId) {
        // The chat we just created is bound to a runner that is offline: drop
        // it so a retry re-creates the chat against the newly selected runner.
        const deadId = currentThreadId;
        sessions.value = sessions.value.filter((s) => s.chatID !== deadId);
        // Only tear down the view if the user is still on the dead chat.
        if (stillActive) {
          closeSession();
          if (router) await router.push("/newchat");
        }
        await deleteSessionFromLocal(deadId);
        useToast().error(t("runners.offlineError"));
      } else {
        pushErrorIfActive(t("runners.offlineError"));
      }
    } else if (res?.conflict) {
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
      resetBusy();
      pushErrorIfActive(t("chat.sessionAlreadyRunning"));
    } else if (res?.injected) {
      // The message went into the live turn of a chat held open for its
      // subagents. It is neither queued nor a new run: the backend records it
      // in the transcript (the `message` event replaces the optimistic bubble)
      // and a status event follows when the turn starts. Keep the busy state
      // until then so the composer does not flicker.
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
    } else if (res?.queued) {
      // Server queued the message (local running state was stale): drop the
      // optimistic bubble. The SSE queue snapshot owns queue display, and the
      // persisted user message arrives via the `message` event when executed.
      // Keep isRunning=true: the server run is active and SSE drives it from here.
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
    } else if (!res) {
      rawMessages.value = rawMessages.value.filter((m) => m.id !== userMsgId);
      resetBusy();
      pushErrorIfActive(t("chat.triggerFailed"));
    }
  };

  return { sendMessage, stopExecution, isStoppingSession };
}
