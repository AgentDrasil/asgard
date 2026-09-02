<script setup lang="ts">
import { ref } from "vue";
import { Icon } from "@iconify/vue";
import type { ChatMessage, AgentInfo } from "../../types";
import { getMessageArtifactFiles } from "../../utils/messageUtils";
import { getAgentIcon, formatPath } from "../../utils/agentUtils";
import { formatTimestamp } from "../../lib/format";
import { sendAskUserReply } from "../../lib/api";
import { parseOptions } from "../../utils/askUserOptions";

const props = withDefaults(
  defineProps<{
    message: ChatMessage;
    sessionId?: string | null;
    activeAgent: AgentInfo | null;
    agents?: AgentInfo[];
    readonly?: boolean;
  }>(),
  {
    readonly: false,
  },
);

const emit = defineEmits<{
  (e: "open-artifact", file: string): void;
  (e: "ask-replied", msgId: string, text: string): void;
}>();

const inlineInput = ref("");
const isSubmitting = ref(false);
const isSubmitted = ref(false);
const submittedText = ref("");

const submitReply = async (textToSubmit?: string) => {
  const text = (textToSubmit ?? inlineInput.value).trim();
  if (!text || !props.sessionId || isSubmitting.value) return;

  isSubmitting.value = true;
  const ok = await sendAskUserReply(props.sessionId, props.message.id, text);
  isSubmitting.value = false;

  if (ok) {
    isSubmitted.value = true;
    submittedText.value = text;
    emit("ask-replied", props.message.id, text);
  }
};

const selectOptionAndReply = (option: string) => {
  inlineInput.value = option;
  submitReply(option);
};
</script>

<template>
  <div class="w-full pl-2 pr-2 my-3 min-w-0">
    <div class="card bg-warning/10 border border-warning/30 shadow-sm p-4 rounded-xl space-y-3">
      <div class="flex items-center gap-2 select-none">
        <Icon
          :icon="getAgentIcon(message.agentName, agents, activeAgent)"
          class="h-5 w-5 shrink-0 text-warning"
        />
        <span class="text-xs font-bold text-base-content">
          {{
            $t("chat.isAsking", {
              agent: message.agentName || activeAgent?.name || $t("chat.agent"),
            })
          }}
        </span>
        <span v-if="message.timestamp" class="text-[10px] font-mono text-base-content/40">
          {{ formatTimestamp(message.timestamp) }}
        </span>
      </div>

      <div class="text-sm font-medium text-base-content whitespace-pre-wrap leading-relaxed">
        {{ message.content }}
      </div>

      <!-- Referenced Artifact Files (click to open in artifact viewer) -->
      <div v-if="getMessageArtifactFiles(message).length > 0" class="space-y-1.5 pt-1">
        <div
          class="text-[11px] font-bold uppercase tracking-wider text-base-content/50 select-none"
        >
          {{ $t("chat.filesToReview") }}
        </div>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="file in getMessageArtifactFiles(message)"
            :key="file"
            @click="emit('open-artifact', file)"
            class="btn btn-xs gap-1.5 bg-base-200/80 hover:bg-warning/20 border border-warning/40 text-base-content font-mono normal-case h-7 min-h-0 px-2.5 max-w-full"
            :title="$t('chat.openArtifactTitle', { file })"
          >
            <Icon icon="octicon:file-code-24" class="h-3.5 w-3.5 shrink-0 text-warning" />
            <span class="truncate max-w-[280px]">{{ formatPath(file) }}</span>
            <span class="text-warning">➔</span>
          </button>
        </div>
      </div>

      <!-- Quick Action Option Buttons -->
      <div
        v-if="
          !readonly && !message.replied && !isSubmitted && parseOptions(message.content).length > 0
        "
        class="flex flex-wrap gap-2 pt-1"
      >
        <button
          v-for="opt in parseOptions(message.content)"
          :key="opt"
          @click="selectOptionAndReply(opt)"
          class="btn btn-xs btn-outline btn-warning hover:btn-warning font-medium transition-all"
          :disabled="isSubmitting"
        >
          {{ opt }}
        </button>
      </div>

      <!-- Inline Reply Box -->
      <div
        v-if="!readonly && !message.replied && !isSubmitted"
        class="flex items-center gap-2 pt-2 border-t border-warning/20"
      >
        <input
          v-model="inlineInput"
          @keydown.enter="submitReply()"
          type="text"
          :placeholder="$t('chat.typeReplyPlaceholder')"
          class="input input-sm input-bordered flex-1 bg-base-100 text-xs text-base-content focus:outline-none focus:border-warning"
          :disabled="isSubmitting"
        />
        <button
          @click="submitReply()"
          class="btn btn-sm btn-warning gap-1 text-xs"
          :disabled="!inlineInput.trim() || isSubmitting"
        >
          <span v-if="isSubmitting" class="loading loading-spinner loading-xs"></span>
          <Icon v-else icon="fluent:send-24-filled" class="h-3.5 w-3.5" />
          {{ $t("chat.reply") }}
        </button>
      </div>
      <div
        v-else-if="message.replied || isSubmitted"
        class="text-xs font-semibold text-success flex items-center gap-1.5 pt-2 border-t border-warning/20"
      >
        <Icon icon="fluent:checkmark-circle-24-filled" class="h-4 w-4" />
        <span>{{
          $t("chat.replied", { text: message.replyText || submittedText || inlineInput })
        }}</span>
      </div>
    </div>
  </div>
</template>
