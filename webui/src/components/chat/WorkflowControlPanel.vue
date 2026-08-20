<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { Icon } from "@iconify/vue";
import type { ChatMessage, AgentInfo } from "../../types";
import { computeWorkflowPanelState } from "../../utils/workflowPanelState";
import { formatPath } from "../../utils/agentUtils";
import { sendAskUserReply } from "../../lib/api";
import { parseOptions } from "../../utils/askUserOptions";
import { getMessageArtifactFiles } from "../../utils/messageUtils";

const props = defineProps<{
  activeAgent: AgentInfo | null;
  loading: boolean;
  workingAgentLabel?: string | null;
  messages: ChatMessage[];
  sessionId?: string | null;
}>();

const emit = defineEmits<{
  (e: "ask-replied", msgId: string, text: string): void;
  (e: "open-artifact", file: string): void;
}>();

const isSubmitting = ref(false);
const selectedIndex = ref(0);

// Per-message input drafts to preserve uncommitted text when paginating between questions
const drafts = ref<Record<string, string>>({});

const state = computed(() =>
  computeWorkflowPanelState({
    running: props.loading,
    messages: props.messages,
    workingAgentLabel: props.workingAgentLabel,
    activeAgentName: props.activeAgent?.name,
  }),
);

// Keep selectedIndex within valid range when pendingMessages list changes
watch(
  () => state.value.pendingMessages.length,
  (len) => {
    if (len === 0) {
      selectedIndex.value = 0;
    } else if (selectedIndex.value >= len) {
      selectedIndex.value = len - 1;
    }
  },
  { immediate: true },
);

// Current pending message being answered
const currentPending = computed<ChatMessage | null>(() => {
  if (state.value.pendingMessages.length === 0) return null;
  const idx = Math.min(Math.max(0, selectedIndex.value), state.value.pendingMessages.length - 1);
  return state.value.pendingMessages[idx] || null;
});

const currentOptions = computed<string[]>(() => {
  return currentPending.value ? parseOptions(currentPending.value.content) : [];
});

const currentArtifactFiles = computed<string[]>(() => {
  return currentPending.value ? getMessageArtifactFiles(currentPending.value) : [];
});

// Display specific agent name in header only if a real agentName is provided on the message
const currentAgentName = computed<string | null>(() => {
  return currentPending.value?.agentName || null;
});

const customInput = computed<string>({
  get: () => {
    const msgId = currentPending.value?.id;
    return msgId ? drafts.value[msgId] || "" : "";
  },
  set: (val: string) => {
    const msgId = currentPending.value?.id;
    if (msgId) {
      drafts.value[msgId] = val;
    }
  },
});

// Reset submitting state and drafts when session changes
watch(
  () => props.sessionId,
  () => {
    isSubmitting.value = false;
    drafts.value = {};
    selectedIndex.value = 0;
  },
);

// Reset submitting state when stage transitions or when switching active pending question.
// Note: In multi-question scenarios, replying to one question transitions currentPending.id to
// the next pending question. Resetting isSubmitting here intentionally dismisses the submitting
// spinner immediately so the user can proceed to answer the next pending decision without delay.
watch(
  () => [state.value.stage, currentPending.value?.id] as const,
  ([newStage, newMsgId], [, oldMsgId]) => {
    if (newStage === "running" || newStage === "completed" || newStage === "failed") {
      isSubmitting.value = false;
    }
    if (newStage === "waiting_human" && newMsgId !== oldMsgId) {
      isSubmitting.value = false;
    }
  },
);

const handleReply = async (text: string) => {
  const replyContent = text.trim();
  const msgId = currentPending.value?.id;
  if (!replyContent || !props.sessionId || !msgId || isSubmitting.value) return;

  isSubmitting.value = true;
  try {
    const ok = await sendAskUserReply(props.sessionId, msgId, replyContent);
    if (ok) {
      delete drafts.value[msgId];
      emit("ask-replied", msgId, replyContent);
    } else {
      isSubmitting.value = false;
    }
  } catch {
    isSubmitting.value = false;
  }
};

const getOptionButtonClass = (opt: string): string => {
  const lower = opt.toLowerCase();
  if (
    lower.includes("proceed") ||
    lower.includes("approve") ||
    lower.includes("yes") ||
    lower.includes("pass")
  ) {
    return "btn-success text-success-content";
  }
  if (
    lower.includes("reject") ||
    lower.includes("stop") ||
    lower.includes("cancel") ||
    lower.includes("abort")
  ) {
    return "btn-error text-error-content";
  }
  if (
    lower.includes("change") ||
    lower.includes("request") ||
    lower.includes("warn") ||
    lower.includes("retry")
  ) {
    return "btn-warning text-warning-content";
  }
  return "btn-outline text-base-content";
};
</script>

<template>
  <div class="w-full px-3 py-2">
    <div
      class="rounded-xl border shadow-sm transition-all duration-200"
      :class="[
        isSubmitting || state.stage === 'running'
          ? 'bg-base-200/80 border-primary/30 p-3'
          : state.stage === 'waiting_human'
            ? 'bg-warning/10 border-warning/40 p-4 shadow-warning/5'
            : state.stage === 'failed'
              ? 'bg-error/10 border-error/30 p-3'
              : state.stage === 'completed'
                ? 'bg-success/10 border-success/30 p-3'
                : 'bg-base-200/50 border-base-300 p-3',
      ]"
    >
      <!-- Submitting / Transient Resuming State -->
      <div
        v-if="isSubmitting"
        class="flex items-center gap-3 py-1 text-sm font-medium text-base-content"
      >
        <span class="loading loading-spinner loading-sm text-primary"></span>
        <span>Resuming workflow and executing next step...</span>
      </div>

      <!-- Stage: Waiting Human Decision -->
      <div v-else-if="state.stage === 'waiting_human'" class="space-y-3">
        <div
          class="flex items-center justify-between gap-2 border-b border-warning/20 pb-2 select-none"
        >
          <div class="flex items-center gap-2 min-w-0">
            <Icon icon="fluent:pause-circle-24-filled" class="h-5 w-5 text-warning shrink-0" />
            <span class="text-sm font-bold text-base-content truncate">
              Workflow Paused · Waiting for Human Decision
            </span>
            <span
              v-if="currentAgentName"
              class="text-xs text-base-content/60 font-mono shrink-0 hidden sm:inline"
            >
              ({{ currentAgentName }})
            </span>
          </div>

          <div class="flex items-center gap-2 shrink-0">
            <!-- Left / Right buttons if multiple questions pending -->
            <div
              v-if="state.pendingMessages.length > 1"
              class="flex items-center gap-1 bg-warning/20 rounded-lg px-1.5 py-0.5"
            >
              <button
                type="button"
                @click="selectedIndex = Math.max(0, selectedIndex - 1)"
                :disabled="selectedIndex <= 0 || isSubmitting"
                class="btn btn-ghost btn-xs btn-square h-5 w-5 min-h-0 text-base-content/70 hover:text-base-content disabled:opacity-30"
                title="Previous decision"
              >
                <Icon icon="lucide:chevron-left" class="h-3.5 w-3.5" />
              </button>
              <span class="text-[11px] font-mono font-bold text-warning-content px-1">
                {{ selectedIndex + 1 }} / {{ state.pendingMessages.length }}
              </span>
              <button
                type="button"
                @click="
                  selectedIndex = Math.min(state.pendingMessages.length - 1, selectedIndex + 1)
                "
                :disabled="selectedIndex >= state.pendingMessages.length - 1 || isSubmitting"
                class="btn btn-ghost btn-xs btn-square h-5 w-5 min-h-0 text-base-content/70 hover:text-base-content disabled:opacity-30"
                title="Next decision"
              >
                <Icon icon="lucide:chevron-right" class="h-3.5 w-3.5" />
              </button>
            </div>
            <span class="badge badge-warning badge-sm font-semibold uppercase shrink-0"
              >Waiting Human</span
            >
          </div>
        </div>

        <!-- Files to review if present in currentPending -->
        <div v-if="currentArtifactFiles.length > 0" class="space-y-1.5 pt-1">
          <div
            class="text-[11px] font-bold uppercase tracking-wider text-base-content/60 select-none"
          >
            Files to review
          </div>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="file in currentArtifactFiles"
              :key="file"
              type="button"
              @click="emit('open-artifact', file)"
              class="btn btn-xs gap-1.5 bg-base-100 hover:bg-warning/20 border border-warning/40 text-base-content font-mono normal-case h-7 min-h-0 px-2.5 max-w-full"
              :title="`Open artifact: ${file}`"
            >
              <Icon icon="octicon:file-code-24" class="h-3.5 w-3.5 shrink-0 text-warning" />
              <span class="truncate max-w-[260px]">{{ formatPath(file) }}</span>
              <span class="text-warning">➔</span>
            </button>
          </div>
        </div>

        <!-- Decision Quick Action Buttons -->
        <div v-if="currentOptions.length > 0" class="flex flex-wrap items-center gap-2 pt-1">
          <button
            v-for="opt in currentOptions"
            :key="opt"
            type="button"
            @click="handleReply(opt)"
            class="btn btn-sm gap-1.5 shadow-sm font-medium transition-all"
            :class="getOptionButtonClass(opt)"
            :disabled="isSubmitting"
          >
            <Icon icon="fluent:checkmark-16-filled" class="h-4 w-4" />
            {{ opt }}
          </button>
        </div>

        <!-- Custom Feedback / Resume Input Box -->
        <div class="flex items-center gap-2 pt-2 border-t border-warning/20">
          <input
            v-model="customInput"
            @keydown.enter="handleReply(customInput)"
            type="text"
            placeholder="Enter custom feedback or additional instructions..."
            class="input input-sm input-bordered flex-1 bg-base-100 text-xs text-base-content focus:outline-none focus:border-warning"
            :disabled="isSubmitting"
          />
          <button
            type="button"
            @click="handleReply(customInput)"
            class="btn btn-sm btn-warning gap-1 text-xs"
            :disabled="!customInput.trim() || isSubmitting"
          >
            <Icon icon="fluent:send-24-filled" class="h-3.5 w-3.5" />
            Resume
          </button>
        </div>
      </div>

      <!-- Stage: Running -->
      <div
        v-else-if="state.stage === 'running'"
        class="flex items-center gap-3 py-1 text-sm font-medium text-base-content"
      >
        <span class="loading loading-spinner loading-sm text-primary"></span>
        <span>{{ workingAgentLabel || activeAgent?.name || "Workflow" }} is running...</span>
      </div>

      <!-- Stage: Failed -->
      <div
        v-else-if="state.stage === 'failed'"
        class="flex items-center justify-between gap-2 py-1"
      >
        <div class="flex items-center gap-2 text-sm font-medium text-error min-w-0">
          <Icon icon="fluent:dismiss-circle-24-filled" class="h-5 w-5 shrink-0" />
          <span class="truncate">{{ state.statusText }}</span>
        </div>
        <span class="badge badge-error badge-sm font-semibold uppercase shrink-0">Failed</span>
      </div>

      <!-- Stage: Completed -->
      <div
        v-else-if="state.stage === 'completed'"
        class="flex items-center justify-between gap-2 py-1"
      >
        <div class="flex items-center gap-2 text-sm font-medium text-success">
          <Icon icon="fluent:checkmark-circle-24-filled" class="h-5 w-5 shrink-0" />
          <span>Workflow completed</span>
        </div>
        <span class="badge badge-success badge-sm font-semibold uppercase">Completed</span>
      </div>

      <!-- Stage: Idle -->
      <div v-else class="flex items-center justify-between gap-2 py-1 text-sm text-base-content/70">
        <div class="flex items-center gap-2 font-medium">
          <Icon icon="fluent:play-circle-24-filled" class="h-5 w-5 text-base-content/50 shrink-0" />
          <span>{{ activeAgent?.name || "Workflow" }} not started</span>
        </div>
        <span class="badge badge-ghost badge-sm uppercase">Idle</span>
      </div>
    </div>
  </div>
</template>
