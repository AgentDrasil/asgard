<script setup lang="ts">
import { ref, watch, nextTick } from "vue";
import type { ChatMessage, AgentInfo } from "../types";

const props = defineProps<{
  messages: ChatMessage[];
  loading: boolean;
  activeAgent: AgentInfo | null;
  runDir: string;
  sessionId?: string | null;
}>();

const bottomRef = ref<HTMLDivElement | null>(null);

// Auto scroll to bottom when new messages arrive
watch(
  () => props.messages,
  async () => {
    await nextTick();
    bottomRef.value?.scrollIntoView({ behavior: "smooth" });
  },
  { deep: true },
);

import { Icon } from "@iconify/vue";
import { marked } from "marked";
import DOMPurify from "dompurify";

marked.setOptions({
  gfm: true,
  breaks: true,
});

const formatContent = (content: string) => {
  if (!content) return "";
  const rawHtml = marked.parse(content) as string;
  return DOMPurify.sanitize(rawHtml);
};

// Track which messages are toggled to show raw Markdown text
const showRawMap = ref<Record<string, boolean>>({});
const toggleRaw = (id: string) => {
  showRawMap.value[id] = !showRawMap.value[id];
};

// Track copy feedback state per message
const copiedMap = ref<Record<string, boolean>>({});
const copyMessage = async (id: string, text: string) => {
  try {
    await navigator.clipboard.writeText(text);
    copiedMap.value[id] = true;
    setTimeout(() => {
      copiedMap.value[id] = false;
    }, 2000);
  } catch (e) {
    console.error("Failed to copy text:", e);
  }
};
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100">
    <!-- Header -->
    <header
      class="px-6 py-4 bg-base-200 border-b border-base-300 flex items-center justify-between shadow-sm shrink-0"
    >
      <div class="space-y-1">
        <h2 class="text-md font-bold text-base-content flex items-center gap-2">
          <span>🤖</span>
          <span class="text-base-content font-bold">{{ activeAgent?.name || "Coding Agent" }}</span>
        </h2>
        <p class="text-xs text-base-content/60 font-mono">
          Workspace:
          <span class="bg-base-300 px-1.5 py-0.5 rounded text-base-content">{{ runDir }}</span>
        </p>
      </div>
      <a
        v-if="sessionId"
        :href="`/api/ttyd/agent-${sessionId}`"
        target="_blank"
        rel="noopener noreferrer"
        class="btn btn-outline btn-sm gap-2 text-xs"
        title="Open Agent Workspace Terminal"
      >
        <Icon icon="mynaui:terminal" class="h-4 w-4" />
        <span>Open Terminal</span>
      </a>
    </header>

    <!-- Message List -->
    <div class="flex-1 overflow-y-auto p-6">
      <div class="max-w-4xl w-full mx-auto space-y-4">
        <div v-for="msg in messages" :key="msg.id" class="w-full">
          <!-- Reasoning / Thinking Balloon -->
          <div v-if="msg.role === 'reasoning'" class="w-full pl-2 pr-12 my-2">
            <details
              open
              class="collapse collapse-arrow bg-base-200/50 border border-dashed border-base-300 rounded-lg"
            >
              <summary
                class="collapse-title text-xs font-semibold text-base-content/65 cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none"
              >
                <span>💭</span> Thinking Process
              </summary>
              <div
                class="collapse-content text-xs font-mono text-base-content/50 whitespace-pre-wrap leading-relaxed"
              >
                {{ msg.content }}
              </div>
            </details>
          </div>

          <!-- Activity / Step / Tool Call Collapsible Box -->
          <div
            v-else-if="msg.role === 'activity' || msg.role === 'tool_call'"
            class="w-full pl-2 pr-2 my-2"
          >
            <div class="flex items-center gap-2 mb-1.5 select-none">
              <span class="text-sm">🤖</span>
              <span class="text-xs font-bold text-base-content/70">
                {{ msg.agentName || activeAgent?.name || "Agent" }}
              </span>
            </div>
            <details
              class="collapse collapse-arrow bg-base-200/40 border border-base-300 rounded-lg text-xs w-full"
            >
              <summary
                class="collapse-title font-mono font-medium text-base-content/70 cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none"
              >
                <span class="text-primary">⚙️</span>
                <span
                  class="badge badge-sm badge-ghost text-[10px] uppercase tracking-wider font-semibold font-sans"
                >
                  {{ msg.activityType || msg.role }}
                </span>
              </summary>
              <div class="collapse-content border-t border-base-300/40 pt-3">
                <pre
                  class="bg-base-200/80 p-3 rounded-lg border border-base-300 overflow-x-auto text-xs font-mono text-base-content/80"
                ><code class="whitespace-pre-wrap">{{ msg.content }}</code></pre>
              </div>
            </details>
          </div>

          <!-- User Chat Bubble -->
          <div v-else-if="msg.role === 'user'" class="chat chat-end">
            <div
              class="chat-header text-[10px] uppercase font-bold text-base-content/40 mb-1 select-none flex items-center gap-1"
            >
              You
            </div>
            <div
              class="chat-bubble chat-bubble-primary text-primary-content border border-primary/20 text-sm leading-relaxed max-w-3xl shadow-sm font-sans whitespace-pre-wrap"
            >
              {{ msg.content }}
            </div>
          </div>

          <!-- Assistant Message (Full-width markdown without chat bubble) -->
          <div v-else class="w-full pl-2 pr-2 py-2 my-1">
            <div class="flex items-center gap-2 mb-2 select-none">
              <span class="text-sm">🤖</span>
              <span class="text-xs font-bold text-base-content/70">
                {{ msg.agentName || activeAgent?.name || "Agent" }}
              </span>
            </div>

            <!-- Raw Markdown vs Rendered HTML -->
            <div v-if="showRawMap[msg.id]" class="my-2">
              <pre
                class="bg-base-200/80 p-3 rounded-lg border border-base-300 overflow-x-auto text-xs font-mono text-base-content/80"
              ><code class="whitespace-pre-wrap">{{ msg.content }}</code></pre>
            </div>
            <div
              v-else
              v-html="formatContent(msg.content)"
              class="font-sans prose prose-sm max-w-none text-base-content leading-relaxed [&_p]:mb-3 [&_pre]:bg-base-200/80 [&_pre]:p-4 [&_pre]:rounded-lg [&_pre]:border [&_pre]:border-base-300 [&_code]:bg-base-200/80 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-warning [&_ul]:list-disc [&_ul]:ml-5 [&_ol]:list-decimal [&_ol]:ml-5 [&_a]:text-primary [&_a]:underline"
            ></div>

            <!-- Action Buttons at bottom: Flip View & Copy (Icon-only) -->
            <div class="flex items-center gap-1 mt-2 select-none">
              <button
                @click="toggleRaw(msg.id)"
                class="btn btn-sm btn-ghost btn-square text-base-content/60 hover:text-base-content"
                :title="showRawMap[msg.id] ? 'Show Rendered HTML' : 'Show Raw Markdown'"
              >
                <Icon
                  :icon="
                    showRawMap[msg.id]
                      ? 'material-symbols:html-rounded'
                      : 'material-symbols:markdown-outline-rounded'
                  "
                  class="w-5 h-5 text-base-content/75"
                />
              </button>

              <button
                @click="copyMessage(msg.id, msg.content)"
                class="btn btn-sm btn-ghost btn-square text-base-content/60 hover:text-base-content"
                :title="copiedMap[msg.id] ? 'Copied!' : 'Copy message content'"
              >
                <Icon
                  :icon="
                    copiedMap[msg.id]
                      ? 'material-symbols:check-circle-outline-rounded'
                      : 'mage:copy'
                  "
                  class="w-5 h-5"
                  :class="copiedMap[msg.id] ? 'text-success' : 'text-base-content/75'"
                />
              </button>
            </div>
          </div>
        </div>

        <!-- Agent Working state -->
        <div
          v-if="loading"
          class="flex items-center gap-2 text-xs text-base-content/50 font-mono pl-2 py-2"
        >
          <span class="loading loading-ring loading-xs text-primary"></span>
          <span>Agent is working...</span>
        </div>

        <div ref="bottomRef"></div>
      </div>
    </div>
  </div>
</template>
