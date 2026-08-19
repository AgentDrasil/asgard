<script setup lang="ts">
import { ref, watch, toRef } from "vue";
import { Icon } from "@iconify/vue";
import type { ChatMessage, AgentInfo } from "../types";
import { formatPath } from "../utils/agentUtils";
import { getDirInfo } from "../lib/api";
import { useShortcuts } from "../composables/useShortcuts";
import { useChatScroll } from "../composables/useChatScroll";
import UserMessage from "./chat/UserMessage.vue";
import AssistantMessage from "./chat/AssistantMessage.vue";
import ActivityMessage from "./chat/ActivityMessage.vue";
import AskUserCard from "./chat/AskUserCard.vue";

const {
  toggleSidebarShortcut,
  toggleArtifactsShortcut,
  toggleDiffShortcut,
  toggleTerminalShortcut,
} = useShortcuts();

const props = withDefaults(
  defineProps<{
    messages: ChatMessage[];
    loading: boolean;
    activeAgent: AgentInfo | null;
    agents?: AgentInfo[];
    runDir: string;
    sessionId?: string | null;
    isDetailsOpen?: boolean;
    isTerminalOpen?: boolean;
    modifiedFiles?: string[];
    isArtifactDrawerOpen?: boolean;
    workingAgentLabel?: string | null;
  }>(),
  {
    isDetailsOpen: true,
    isTerminalOpen: false,
    modifiedFiles: () => [],
    isArtifactDrawerOpen: false,
    workingAgentLabel: null,
  },
);

const emit = defineEmits<{
  (e: "update:isDetailsOpen", val: boolean): void;
  (e: "open-diff", gitRoot: string): void;
  (e: "open-artifact", file: string): void;
  (e: "toggle-terminal"): void;
  (e: "toggle-sidebar"): void;
  (e: "toggle-artifact-drawer"): void;
  (e: "ask-replied", msgId?: string, text?: string): void;
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

const { showScrollBottom, scrollToBottom } = useChatScroll({
  messages: toRef(props, "messages"),
  sessionId: toRef(props, "sessionId"),
  isDetailsOpen: toRef(props, "isDetailsOpen"),
  onUpdateDetailsOpen: (open) => emit("update:isDetailsOpen", open),
});
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100 min-w-0 relative">
    <!-- Header -->
    <header
      class="px-3 py-2 sm:px-6 sm:py-3 bg-base-200 border-b border-base-300 flex items-start justify-between shadow-sm shrink-0 min-w-0 transition-all duration-200"
    >
      <div class="flex items-start gap-2 min-w-0 pr-2">
        <button
          @click="emit('toggle-sidebar')"
          class="md:hidden btn btn-ghost btn-xs btn-square text-base-content/80 shrink-0 mt-0.5"
          :title="`Toggle Menu (${toggleSidebarShortcut})`"
        >
          <Icon icon="mynaui:sidebar" class="h-5 w-5" />
        </button>

        <div class="space-y-1 min-w-0">
          <button
            @click="emit('update:isDetailsOpen', !isDetailsOpen)"
            class="flex items-center gap-2 text-sm sm:text-md font-bold text-base-content hover:text-primary transition-colors cursor-pointer select-none text-left truncate h-7 sm:h-8"
            title="Toggle Workspace Info"
          >
            <Icon :icon="activeAgent?.icon || 'fluent-color:bot-24'" class="h-5 w-5 shrink-0" />
            <span class="font-bold truncate">{{ activeAgent?.name || "Coding Agent" }}</span>
            <Icon
              :icon="isDetailsOpen ? 'ep:arrow-up' : 'ep:arrow-down'"
              class="h-3.5 w-3.5 text-base-content/70 shrink-0"
            />
          </button>

          <div v-if="isDetailsOpen" class="flex flex-wrap items-center gap-x-4 gap-y-1 pt-0.5">
            <p class="text-[11px] sm:text-xs text-base-content/60 font-mono truncate">
              Workspace:
              <span class="bg-base-300 px-1.5 py-0.5 rounded text-base-content truncate">{{
                formatPath(runDir)
              }}</span>
            </p>
            <p
              v-if="gitRoot"
              class="text-[11px] sm:text-xs text-base-content/60 font-mono truncate"
            >
              Git Root:
              <span class="bg-base-300 px-1.5 py-0.5 rounded text-base-content truncate">{{
                formatPath(gitRoot)
              }}</span>
            </p>
          </div>
        </div>
      </div>

      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0 h-7 sm:h-8">
        <!-- View Switcher Join Group (Chat / VCS) -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <button
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-primary shadow-xs"
            title="Chat View"
          >
            <Icon icon="material-symbols:chat-outline" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">Chat</span>
          </button>
          <button
            v-if="gitRoot"
            @click="emit('open-diff', gitRoot)"
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-ghost text-base-content/70 hover:text-base-content"
            :title="`Switch to VCS View (${toggleDiffShortcut})`"
          >
            <Icon icon="octicon:git-branch-24" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">VCS</span>
          </button>
        </div>

        <!-- Layout Controls Join Group (Bottom Panel / Right Sidebar) -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <!-- Toggle Terminal Bottom Panel Button (VS Code style) -->
          <button
            v-if="sessionId"
            @click="emit('toggle-terminal')"
            class="join-item btn btn-xs border-none gap-1"
            :class="
              isTerminalOpen
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content'
            "
            :title="`Toggle Terminal Panel (${toggleTerminalShortcut})`"
          >
            <Icon icon="codicon:layout-panel" class="h-3.5 w-3.5" />
            <span class="hidden xl:inline">Terminal</span>
          </button>

          <!-- Toggle Artifacts Right Sidebar Button -->
          <button
            v-if="sessionId"
            @click="emit('toggle-artifact-drawer')"
            class="join-item btn btn-xs border-none gap-1"
            :class="
              isArtifactDrawerOpen
                ? 'btn-primary shadow-xs'
                : modifiedFiles && modifiedFiles.length > 0
                  ? 'btn-secondary text-secondary-content shadow-xs font-semibold'
                  : 'btn-ghost text-base-content/50 hover:text-base-content'
            "
            :title="`Toggle Artifacts Sidebar (${toggleArtifactsShortcut})`"
          >
            <Icon icon="codicon:layout-sidebar-right" class="h-3.5 w-3.5" />
            <span class="hidden xl:inline">Artifacts</span>
            <span
              v-if="modifiedFiles && modifiedFiles.length > 0"
              class="badge badge-xs font-bold"
              :class="
                isArtifactDrawerOpen
                  ? 'bg-base-100/30 text-current border-none'
                  : 'bg-secondary-content/20 text-secondary-content border-none'
              "
            >
              {{ modifiedFiles.length }}
            </span>
          </button>
        </div>
      </div>
    </header>

    <!-- Message List -->
    <div
      ref="scrollContainerRef"
      class="flex-1 overflow-y-auto overflow-x-hidden p-3 sm:p-6 min-w-0 w-full"
    >
      <div class="max-w-4xl w-full mx-auto space-y-4 min-w-0">
        <div v-for="msg in messages" :key="msg.id" class="w-full min-w-0">
          <!-- Ask User Question Box -->
          <AskUserCard
            v-if="msg.role === 'ask_user'"
            :message="msg"
            :session-id="sessionId"
            :active-agent="activeAgent"
            :agents="agents"
            @open-artifact="emit('open-artifact', $event)"
            @ask-replied="(id, text) => emit('ask-replied', id, text)"
          />

          <!-- User Chat Bubble -->
          <UserMessage v-else-if="msg.role === 'user'" :message="msg" />

          <!-- Activity / Tool / Reasoning / Error -->
          <ActivityMessage
            v-else-if="
              msg.role === 'activity' ||
              msg.role === 'tool_call' ||
              msg.role === 'tool_result' ||
              msg.role === 'reasoning' ||
              msg.role === 'error'
            "
            :message="msg"
            :active-agent="activeAgent"
            :agents="agents"
            @open-artifact="emit('open-artifact', $event)"
          />

          <!-- Assistant Message -->
          <AssistantMessage v-else :message="msg" :active-agent="activeAgent" :agents="agents" />
        </div>

        <!-- Agent Working state -->
        <div
          v-if="loading"
          class="flex items-center gap-2 text-xs text-base-content/50 font-mono pl-2 py-2"
        >
          <span class="loading loading-ring loading-xs text-primary"></span>
          <span>
            Agent ({{ workingAgentLabel || activeAgent?.name || "Agent" }}) is working...
          </span>
        </div>
      </div>
    </div>

    <!-- Scroll to bottom button -->
    <Transition
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="opacity-0 translate-y-2 scale-95"
      enter-to-class="opacity-100 translate-y-0 scale-100"
      leave-active-class="transition duration-150 ease-in"
      leave-from-class="opacity-100 translate-y-0 scale-100"
      leave-to-class="opacity-0 translate-y-2 scale-95"
    >
      <button
        v-if="showScrollBottom"
        @click="scrollToBottom"
        class="absolute bottom-4 right-4 sm:bottom-6 sm:right-6 z-10 btn btn-circle btn-sm sm:btn-md bg-base-200 hover:bg-base-300 border border-base-300 shadow-lg text-base-content"
        title="Scroll to bottom"
        aria-label="Scroll to bottom"
      >
        <Icon icon="ep:arrow-down-bold" class="h-4 w-4 sm:h-5 sm:w-5" />
      </button>
    </Transition>
  </div>
</template>
