<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { Icon } from "@iconify/vue";
import type { ChatMessage, AgentInfo } from "../../types";
import { computeWorkflowPanelState } from "../../utils/workflowPanelState";
import { formatPath } from "../../utils/agentUtils";
import { sendAskUserReply } from "../../lib/api";

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
const customInput = ref("");

const state = computed(() =>
  computeWorkflowPanelState({
    running: props.loading,
    messages: props.messages,
    workingAgentLabel: props.workingAgentLabel,
    activeAgentName: props.activeAgent?.name,
  }),
);

// Reset submitting state when session changes
watch(
  () => props.sessionId,
  () => {
    isSubmitting.value = false;
    customInput.value = "";
  },
);

// Reset submitting state when stage transitions or a new waiting_human message arrives
watch(
  () => [state.value.stage, state.value.pendingMessage?.id] as const,
  ([newStage, newMsgId], [, oldMsgId]) => {
    if (newStage === "running" || newStage === "completed" || newStage === "failed") {
      isSubmitting.value = false;
    }
    if (newStage === "waiting_human" && newMsgId !== oldMsgId) {
      isSubmitting.value = false;
      customInput.value = "";
    }
  },
);

const handleReply = async (text: string) => {
  const replyContent = text.trim();
  const msgId = state.value.pendingMessage?.id;
  if (!replyContent || !props.sessionId || !msgId || isSubmitting.value) return;

  isSubmitting.value = true;
  try {
    const ok = await sendAskUserReply(props.sessionId, msgId, replyContent);
    if (ok) {
      customInput.value = "";
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
        <span>正在恢复工作流并执行下一步...</span>
      </div>

      <!-- Stage: Waiting Human Decision -->
      <div v-else-if="state.stage === 'waiting_human'" class="space-y-3">
        <div
          class="flex items-center justify-between gap-2 border-b border-warning/20 pb-2 select-none"
        >
          <div class="flex items-center gap-2">
            <Icon icon="fluent:pause-circle-24-filled" class="h-5 w-5 text-warning shrink-0" />
            <span class="text-sm font-bold text-base-content"> Workflow 暂停 · 等待人工决策 </span>
          </div>
          <span class="badge badge-warning badge-sm font-semibold uppercase">Waiting Human</span>
        </div>

        <div
          v-if="state.pendingMessage"
          class="text-xs text-base-content whitespace-pre-wrap leading-relaxed"
        >
          {{ state.pendingMessage.content }}
        </div>

        <!-- Files to review if present in pendingMessage -->
        <div v-if="state.artifactFiles.length > 0" class="space-y-1.5 pt-1">
          <div
            class="text-[11px] font-bold uppercase tracking-wider text-base-content/60 select-none"
          >
            待审阅产物 (Files to review)
          </div>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="file in state.artifactFiles"
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
        <div v-if="state.options.length > 0" class="flex flex-wrap items-center gap-2 pt-1">
          <button
            v-for="opt in state.options"
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
            placeholder="输入自定义反馈或附加要求..."
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
        <span>{{ workingAgentLabel || activeAgent?.name || "Workflow" }} 正在运行中...</span>
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
          <span>工作流执行完成</span>
        </div>
        <span class="badge badge-success badge-sm font-semibold uppercase">Completed</span>
      </div>

      <!-- Stage: Idle -->
      <div v-else class="flex items-center justify-between gap-2 py-1 text-sm text-base-content/70">
        <div class="flex items-center gap-2 font-medium">
          <Icon icon="fluent:play-circle-24-filled" class="h-5 w-5 text-base-content/50 shrink-0" />
          <span>{{ activeAgent?.name || "工作流" }} 尚未启动</span>
        </div>
        <span class="badge badge-ghost badge-sm uppercase">Idle</span>
      </div>
    </div>
  </div>
</template>
