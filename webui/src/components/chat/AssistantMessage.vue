<script setup lang="ts">
import { ref } from "vue";
import { Icon } from "@iconify/vue";
import DOMPurify from "dompurify";
import type { ChatMessage, AgentInfo } from "../../types";
import { formatContextUsage, getContextColorClass, formatTimestamp } from "../../lib/format";
import { getAgentIcon } from "../../utils/agentUtils";
import { useShiki } from "../../composables/useShiki";
import MarkdownContent from "../MarkdownContent.vue";

defineProps<{
  message: ChatMessage;
  activeAgent: AgentInfo | null;
  agents?: AgentInfo[];
}>();

const { highlightBlock } = useShiki();

const showRaw = ref(false);
const toggleRaw = () => {
  showRaw.value = !showRaw.value;
};

const copied = ref(false);
const copyMessage = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text);
    copied.value = true;
    setTimeout(() => {
      copied.value = false;
    }, 2000);
  } catch (e) {
    console.error("Failed to copy text:", e);
  }
};

const BLOCK_CLASSES = [
  "rounded-lg",
  "p-4",
  "overflow-x-auto",
  "my-2",
  "border",
  "border-base-300",
  "text-xs",
  "font-mono",
] as const;

const formatRawMarkdown = (content: string) => {
  if (!content) return "";
  const highlighted = highlightBlock(content, "markdown", [...BLOCK_CLASSES]);
  if (highlighted) return highlighted;
  return `<pre class="bg-base-200/80 p-3 rounded-lg border border-base-300 overflow-x-auto max-w-full min-w-0 text-xs font-mono text-base-content/80"><code class="whitespace-pre-wrap break-words [word-break:break-word]">${DOMPurify.sanitize(content)}</code></pre>`;
};
</script>

<template>
  <div class="w-full pl-2 pr-2 py-2 my-1 min-w-0">
    <div class="flex items-center gap-2 mb-2 select-none">
      <Icon :icon="getAgentIcon(message.agentName, agents, activeAgent)" class="h-4 w-4 shrink-0" />
      <span class="text-xs font-bold text-base-content/70">
        {{ message.agentName || activeAgent?.name || $t("chat.agent") }}
      </span>
      <span v-if="message.timestamp" class="text-[10px] font-mono text-base-content/40">
        {{ formatTimestamp(message.timestamp) }}
      </span>
    </div>

    <!-- Raw Markdown vs Rendered HTML -->
    <div
      v-if="showRaw"
      v-html="formatRawMarkdown(message.content)"
      class="my-2 min-w-0 font-mono text-xs overflow-x-auto"
    ></div>
    <MarkdownContent v-else :content="message.content" />

    <!-- Action Buttons at bottom: Flip View & Copy (Icon-only) -->
    <div class="flex items-center gap-1 mt-2 select-none">
      <button
        @click="toggleRaw"
        class="btn btn-sm btn-ghost btn-square text-base-content/60 hover:text-base-content"
        :title="showRaw ? $t('chat.showRenderedHtml') : $t('chat.showRawMarkdown')"
      >
        <Icon
          :icon="
            showRaw ? 'material-symbols:html-rounded' : 'material-symbols:markdown-outline-rounded'
          "
          class="w-5 h-5 text-base-content/75"
        />
      </button>

      <button
        @click="copyMessage(message.content)"
        class="btn btn-sm btn-ghost btn-square text-base-content/60 hover:text-base-content"
        :title="copied ? $t('chat.copied') : $t('chat.copyMessageContent')"
      >
        <Icon
          :icon="copied ? 'material-symbols:check-circle-outline-rounded' : 'mage:copy'"
          class="w-5 h-5"
          :class="copied ? 'text-success' : 'text-base-content/75'"
        />
      </button>

      <span
        v-if="message.inputTokens && message.maxTokens"
        class="text-xs font-mono ml-1.5 px-2 py-0.5 rounded cursor-default select-none transition-colors"
        :class="getContextColorClass(message.inputTokens, message.maxTokens)"
        :title="`${message.inputTokens.toLocaleString()} / ${message.maxTokens.toLocaleString()} tokens`"
      >
        {{ formatContextUsage(message.inputTokens, message.maxTokens) }}
      </span>
    </div>
  </div>
</template>
