<script setup lang="ts">
import { ref, watch, nextTick } from "vue";
import type { ChatMessage, AgentInfo } from "../types";

const props = defineProps<{
  messages: ChatMessage[];
  loading: boolean;
  activeAgent: AgentInfo | null;
  runDir: string;
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

// Simple markdown formatter to display formatted content safely
const formatContent = (content: string) => {
  if (!content) return "";

  // Escape HTML characters to prevent XSS
  let html = content.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

  // Format code blocks: ```lang ... ```
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
    return `<div class="mockup-code bg-base-300 my-3 text-sm font-mono relative overflow-x-auto">
      <div class="px-5 text-xs text-base-content/40 absolute right-3 top-2">${lang || "code"}</div>
      <pre class="px-5"><code>${code.trim()}</code></pre>
    </div>`;
  });

  // Format inline code: `code`
  html = html.replace(
    /`([^`]+)`/g,
    '<code class="bg-base-300 text-warning px-1.5 py-0.5 rounded font-mono text-xs">$1</code>',
  );

  // Format bold text: **bold**
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong class="font-bold text-white">$1</strong>');

  // Format line breaks
  html = html
    .split("\n")
    .map((line) => {
      if (line.startsWith("- ") || line.startsWith("* ")) {
        return `<li class="ml-4 list-disc">${line.slice(2)}</li>`;
      }
      return line ? `<p class="mb-2">${line}</p>` : '<div class="h-2"></div>';
    })
    .join("");

  return html;
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
    </header>

    <!-- Message List -->
    <div class="flex-1 overflow-y-auto p-6 space-y-4">
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

        <!-- Activity / Step Badge (including tool_call) -->
        <div v-else-if="msg.role === 'activity' || msg.role === 'tool_call'" class="w-full pl-2 my-1">
          <div
            class="inline-flex items-center gap-2 text-xs font-mono bg-cyan-950/40 text-cyan-400 border border-cyan-800/40 px-3 py-1 rounded-lg"
          >
            <span>⚙️</span>
            <span v-if="msg.agentName" class="font-bold text-cyan-300">{{ msg.agentName }}:</span>
            <span>[{{ msg.activityType || msg.role.toUpperCase() }}]</span>
            <span>{{ msg.content }}</span>
          </div>
        </div>

        <!-- Standard Chat Bubbles -->
        <div v-else :class="['chat', msg.role === 'user' ? 'chat-end' : 'chat-start']">
          <div
            class="chat-header text-[10px] uppercase font-bold text-base-content/40 mb-1 select-none flex items-center gap-1"
          >
            <span v-if="msg.role === 'user'">You</span>
            <span v-else>{{ msg.agentName || activeAgent?.name || "Agent" }}</span>
          </div>

          <div
            :class="[
              'chat-bubble text-sm leading-relaxed max-w-3xl shadow-sm border',
              msg.role === 'user'
                ? 'chat-bubble-primary text-primary-content border-primary/20'
                : 'bg-base-200 text-base-content border-base-300',
            ]"
          >
            <!-- User messages: Plain Text -->
            <div v-if="msg.role === 'user'" class="whitespace-pre-wrap font-sans">
              {{ msg.content }}
            </div>

            <!-- Assistant/Other: Formatted HTML Markdown -->
            <div v-else v-html="formatContent(msg.content)" class="font-sans"></div>
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
</template>
