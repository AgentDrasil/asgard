<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted } from "vue";
import type { ChatMessage, AgentInfo } from "../types";
import { getDirInfo, sendAskUserReply } from "../lib/api";
import { formatContextUsage, getContextColorClass } from "../lib/format";
import { TOOL_ITEM_DELIMITER } from "../utils/messageUtils";

const inlineInputMap = ref<Record<string, string>>({});
const inlineSubmittingMap = ref<Record<string, boolean>>({});
const inlineSubmittedMap = ref<Record<string, boolean>>({});

const submitInlineReply = async (msgId: string) => {
  const text = inlineInputMap.value[msgId]?.trim();
  if (!text || !props.sessionId || inlineSubmittingMap.value[msgId]) return;

  inlineSubmittingMap.value[msgId] = true;
  const ok = await sendAskUserReply(props.sessionId, msgId, text);
  inlineSubmittingMap.value[msgId] = false;

  if (ok) {
    inlineSubmittedMap.value[msgId] = true;
    const targetMsg = props.messages.find((m) => m.id === msgId);
    if (targetMsg) {
      targetMsg.replied = true;
      targetMsg.replyText = text;
    }
  }
};

const props = withDefaults(
  defineProps<{
    messages: ChatMessage[];
    loading: boolean;
    activeAgent: AgentInfo | null;
    runDir: string;
    sessionId?: string | null;
    isDetailsOpen?: boolean;
    isTerminalOpen?: boolean;
  }>(),
  {
    isDetailsOpen: true,
    isTerminalOpen: false,
  },
);

const emit = defineEmits<{
  (e: "update:isDetailsOpen", val: boolean): void;
  (e: "open-diff", gitRoot: string): void;
  (e: "toggle-terminal"): void;
}>();

const gitRoot = ref("");

watch(
  () => props.runDir,
  async (newDir) => {
    if (!newDir) {
      gitRoot.value = "";
      return;
    }
    const info = await getDirInfo(newDir);
    gitRoot.value = info.gitRoot || "";
  },
  { immediate: true },
);

const bottomRef = ref<HTMLDivElement | null>(null);
const scrollContainerRef = ref<HTMLDivElement | null>(null);
let lastAtTopState = props.isDetailsOpen;
let ticking = false;

const checkScrollPosition = () => {
  if (!scrollContainerRef.value) return;
  const atTop = scrollContainerRef.value.scrollTop <= 5;
  if (atTop !== lastAtTopState) {
    lastAtTopState = atTop;
    emit("update:isDetailsOpen", atTop);
  }
};

const handleScroll = () => {
  if (!ticking) {
    requestAnimationFrame(() => {
      checkScrollPosition();
      ticking = false;
    });
    ticking = true;
  }
};

watch(
  () => props.isDetailsOpen,
  (newVal) => {
    lastAtTopState = newVal;
  },
);

onMounted(() => {
  const el = scrollContainerRef.value;
  if (el) {
    el.addEventListener("scroll", handleScroll, { passive: true });
    nextTick(() => {
      checkScrollPosition();
    });
  }
});

onUnmounted(() => {
  const el = scrollContainerRef.value;
  if (el) {
    el.removeEventListener("scroll", handleScroll);
  }
});

// Auto scroll to bottom when new messages arrive and re-check scroll position
watch(
  [() => props.sessionId, () => props.messages],
  async () => {
    await nextTick();
    bottomRef.value?.scrollIntoView({ behavior: "smooth" });
    checkScrollPosition();
    setTimeout(() => {
      checkScrollPosition();
    }, 150);
  },
  { deep: true, immediate: true },
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
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100 min-w-0">
    <!-- Header -->
    <header
      class="px-3 py-2 sm:px-6 sm:py-3 bg-base-200 border-b border-base-300 flex items-center justify-between shadow-sm shrink-0 min-w-0 transition-all duration-200"
      :class="isDetailsOpen ? 'flex' : 'hidden md:flex'"
    >
      <div class="space-y-0.5 sm:space-y-1 min-w-0 pr-2">
        <button
          @click="emit('update:isDetailsOpen', !isDetailsOpen)"
          class="hidden md:flex items-center gap-2 text-sm sm:text-md font-bold text-base-content hover:text-primary transition-colors cursor-pointer select-none text-left truncate"
          title="Toggle Workspace Info"
        >
          <Icon :icon="activeAgent?.icon || 'fluent-color:bot-24'" class="h-5 w-5 shrink-0" />
          <span class="font-bold truncate">{{ activeAgent?.name || "Coding Agent" }}</span>
          <Icon
            :icon="isDetailsOpen ? 'ep:arrow-up' : 'ep:arrow-down'"
            class="h-3.5 w-3.5 text-base-content/70 shrink-0"
          />
        </button>
        <div v-if="isDetailsOpen" class="space-y-0.5 sm:space-y-1">
          <p class="text-[11px] sm:text-xs text-base-content/60 font-mono truncate">
            Workspace:
            <span class="bg-base-300 px-1.5 py-0.5 rounded text-base-content truncate">{{
              runDir
            }}</span>
          </p>
          <p v-if="gitRoot" class="text-[11px] sm:text-xs text-base-content/60 font-mono truncate">
            Git Root:
            <span class="bg-base-300 px-1.5 py-0.5 rounded text-base-content truncate">{{
              gitRoot
            }}</span>
          </p>
        </div>
      </div>
      <div class="flex items-center gap-1 sm:gap-2 shrink-0">
        <!-- Open Diff (only in git repos) -->
        <button
          v-if="gitRoot"
          @click="emit('open-diff', gitRoot)"
          class="btn btn-outline btn-xs sm:btn-sm gap-1 sm:gap-2 text-xs"
          title="Open Git Diff View"
        >
          <Icon icon="octicon:file-diff-24" class="h-4 w-4" />
          <span class="hidden sm:inline">Open Diff</span>
        </button>

        <!-- Open Terminal -->
        <button
          v-if="sessionId"
          @click="emit('toggle-terminal')"
          class="btn btn-outline btn-xs sm:btn-sm gap-1 sm:gap-2 text-xs"
          :class="{ 'btn-active btn-primary': isTerminalOpen }"
          title="Toggle Agent Workspace Terminal (Ctrl+`)"
        >
          <Icon icon="mynaui:terminal" class="h-4 w-4" />
          <span class="hidden sm:inline">{{
            isTerminalOpen ? "Hide Terminal" : "Open Terminal"
          }}</span>
        </button>
      </div>
    </header>

    <!-- Message List -->
    <div
      ref="scrollContainerRef"
      class="flex-1 overflow-y-auto overflow-x-hidden p-3 sm:p-6 min-w-0 w-full"
    >
      <div class="max-w-4xl w-full mx-auto space-y-4 min-w-0">
        <div v-for="msg in messages" :key="msg.id" class="w-full min-w-0">
          <!-- Reasoning / Thinking Balloon -->
          <div v-if="msg.role === 'reasoning'" class="w-full sm:pl-2 sm:pr-12 my-2 min-w-0">
            <details
              open
              class="collapse collapse-arrow bg-base-200/50 border border-dashed border-base-300 rounded-lg min-w-0"
            >
              <summary
                class="collapse-title text-xs font-semibold text-base-content/65 cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none"
              >
                <span>💭</span> Thinking Process
              </summary>
              <div
                class="collapse-content text-xs font-mono text-base-content/50 whitespace-pre-wrap leading-relaxed break-words [word-break:break-word]"
              >
                {{ msg.content }}
              </div>
            </details>
          </div>

          <!-- Activity / Step / Tool Call Collapsible Box -->
          <div
            v-else-if="
              msg.role === 'activity' || msg.role === 'tool_call' || msg.role === 'tool_result'
            "
            class="w-full pl-2 pr-2 my-2 min-w-0"
          >
            <div class="flex items-center gap-2 mb-1.5 select-none">
              <Icon :icon="activeAgent?.icon || 'fluent-color:bot-24'" class="h-4 w-4 shrink-0" />
              <span class="text-xs font-bold text-base-content/70">
                {{ msg.agentName || activeAgent?.name || "Agent" }}
              </span>
            </div>
            <details
              class="collapse collapse-arrow bg-base-200/40 border border-base-300 rounded-lg text-xs w-full min-w-0"
            >
              <summary
                class="collapse-title font-mono font-medium text-base-content/70 cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none"
              >
                <span class="text-primary">⚙️</span>
                <span
                  class="badge badge-sm badge-ghost text-[10px] uppercase tracking-wider font-semibold font-sans"
                >
                  {{
                    (msg.activityType || msg.role) === "TOOL_CALL" ||
                    (msg.activityType || msg.role) === "tool_call" ||
                    (msg.activityType || msg.role) === "tool_result"
                      ? "TOOL"
                      : msg.activityType || msg.role
                  }}
                </span>
              </summary>
              <div class="collapse-content border-t border-base-300/40 pt-3 space-y-2 min-w-0">
                <template
                  v-for="(item, idx) in msg.content.includes(TOOL_ITEM_DELIMITER)
                    ? msg.content.split(TOOL_ITEM_DELIMITER)
                    : msg.content.split('\n\n')"
                  :key="idx"
                >
                  <pre
                    v-if="item.trim()"
                    class="bg-base-200/80 p-3 rounded-lg border border-base-300 overflow-x-auto max-w-full min-w-0 text-xs font-mono text-base-content/80"
                  ><code class="whitespace-pre-wrap break-words [word-break:break-word]">{{ item.trim() }}</code></pre>
                </template>
              </div>
            </details>
          </div>

          <!-- Ask User Question Box with Inline Reply -->
          <div v-else-if="msg.role === 'ask_user'" class="w-full pl-2 pr-2 my-3 min-w-0">
            <div
              class="card bg-warning/10 border border-warning/30 shadow-sm p-4 rounded-xl space-y-3"
            >
              <div class="flex items-center gap-2 select-none">
                <Icon
                  :icon="activeAgent?.icon || 'fluent-color:bot-24'"
                  class="h-5 w-5 shrink-0 text-warning"
                />
                <span class="text-xs font-bold text-base-content">
                  {{ msg.agentName || activeAgent?.name || "Agent" }} is asking:
                </span>
              </div>
              <div
                class="text-sm font-medium text-base-content whitespace-pre-wrap leading-relaxed"
              >
                {{ msg.content }}
              </div>

              <!-- Inline Reply Box -->
              <div
                v-if="!msg.replied && !inlineSubmittedMap[msg.id]"
                class="flex items-center gap-2 pt-2 border-t border-warning/20"
              >
                <input
                  v-model="inlineInputMap[msg.id]"
                  @keydown.enter="submitInlineReply(msg.id)"
                  type="text"
                  placeholder="Type your reply to agent..."
                  class="input input-sm input-bordered flex-1 bg-base-100 text-xs text-base-content focus:outline-none focus:border-warning"
                  :disabled="inlineSubmittingMap[msg.id]"
                />
                <button
                  @click="submitInlineReply(msg.id)"
                  class="btn btn-sm btn-warning gap-1 text-xs"
                  :disabled="!inlineInputMap[msg.id]?.trim() || inlineSubmittingMap[msg.id]"
                >
                  <span
                    v-if="inlineSubmittingMap[msg.id]"
                    class="loading loading-spinner loading-xs"
                  ></span>
                  <Icon v-else icon="fluent:send-24-filled" class="h-3.5 w-3.5" />
                  Reply
                </button>
              </div>
              <div
                v-else
                class="text-xs font-semibold text-success flex items-center gap-1.5 pt-2 border-t border-warning/20"
              >
                <Icon icon="fluent:checkmark-circle-24-filled" class="h-4 w-4" />
                <span>Replied: {{ msg.replyText || inlineInputMap[msg.id] }}</span>
              </div>
            </div>
          </div>

          <!-- User Chat Bubble -->
          <div v-else-if="msg.role === 'user'" class="chat chat-end min-w-0">
            <div
              class="chat-header text-[10px] uppercase font-bold text-base-content/40 mb-1 select-none flex items-center gap-1"
            >
              You
            </div>
            <div
              class="chat-bubble chat-bubble-primary text-primary-content border border-primary/20 text-sm leading-relaxed max-w-3xl shadow-sm font-sans whitespace-pre-wrap break-words [word-break:break-word] min-w-0"
            >
              {{ msg.content }}
            </div>
          </div>

          <!-- Assistant Message (Full-width markdown without chat bubble) -->
          <div v-else class="w-full pl-2 pr-2 py-2 my-1 min-w-0">
            <div class="flex items-center gap-2 mb-2 select-none">
              <Icon :icon="activeAgent?.icon || 'fluent-color:bot-24'" class="h-4 w-4 shrink-0" />
              <span class="text-xs font-bold text-base-content/70">
                {{ msg.agentName || activeAgent?.name || "Agent" }}
              </span>
            </div>

            <!-- Raw Markdown vs Rendered HTML -->
            <div v-if="showRawMap[msg.id]" class="my-2 min-w-0">
              <pre
                class="bg-base-200/80 p-3 rounded-lg border border-base-300 overflow-x-auto max-w-full min-w-0 text-xs font-mono text-base-content/80"
              ><code class="whitespace-pre-wrap break-words [word-break:break-word]">{{ msg.content }}</code></pre>
            </div>
            <div
              v-else
              v-html="formatContent(msg.content)"
              class="font-sans prose prose-sm max-w-none text-base-content leading-relaxed min-w-0 break-words [word-break:break-word] [&_p]:mb-3 [&_hr]:my-4 [&_hr]:border-t [&_hr]:border-base-content/20 [&_pre]:bg-base-200/80 [&_pre]:p-4 [&_pre]:rounded-lg [&_pre]:border [&_pre]:border-base-300 [&_pre]:overflow-x-auto [&_pre]:max-w-full [&_code]:bg-base-200/80 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-warning [&_code]:break-words [&_ul]:list-disc [&_ul]:ml-5 [&_ol]:list-decimal [&_ol]:ml-5 [&_a]:text-primary [&_a]:underline [&_table]:block [&_table]:overflow-x-auto [&_table]:max-w-full"
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

              <span
                v-if="msg.inputTokens && msg.maxTokens"
                class="text-xs font-mono ml-1.5 px-2 py-0.5 rounded cursor-default select-none transition-colors"
                :class="getContextColorClass(msg.inputTokens, msg.maxTokens)"
                :title="`${msg.inputTokens.toLocaleString()} / ${msg.maxTokens.toLocaleString()} tokens`"
              >
                {{ formatContextUsage(msg.inputTokens, msg.maxTokens) }}
              </span>
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
