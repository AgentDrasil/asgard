<script setup lang="ts">
import ChatArea from "../components/ChatArea.vue";
import ChatInput from "../components/ChatInput.vue";
import DiffView from "../components/DiffView.vue";
import TerminalPanel from "../components/TerminalPanel.vue";
import type { ChatMessage, AgentInfo } from "../types";

defineProps<{
  messages: ChatMessage[];
  loading: boolean;
  activeAgent: AgentInfo | null;
  runDir: string;
  sessionId: string;
  showDiffView: boolean;
  gitRoot: string;
  terminalType?: "session" | "sidebar";
}>();

const isDetailsOpen = defineModel<boolean>("isDetailsOpen");
const chatInputText = defineModel<string>("chatInputText");
const isTerminalOpen = defineModel<boolean>("isTerminalOpen", { default: false });

const emit = defineEmits<{
  (e: "send", text: string): void;
  (e: "open-diff", gitRoot: string): void;
  (e: "close-diff"): void;
  (e: "toggle-terminal"): void;
}>();
</script>

<template>
  <DiffView
    v-if="showDiffView"
    :runDir="runDir"
    :gitRoot="gitRoot"
    v-model:chatInputText="chatInputText"
    @close="$emit('close-diff')"
  />
  <div v-else class="flex-1 flex flex-col h-full overflow-hidden relative">
    <ChatArea
      :messages="messages"
      :loading="loading"
      :activeAgent="activeAgent"
      :runDir="runDir"
      :sessionId="sessionId"
      :isTerminalOpen="isTerminalOpen"
      v-model:isDetailsOpen="isDetailsOpen"
      @open-diff="(g) => $emit('open-diff', g)"
      @toggle-terminal="emit('toggle-terminal')"
    />
    <ChatInput @send="(text) => $emit('send', text)" :loading="loading" v-model="chatInputText" />
    <TerminalPanel
      :sessionId="sessionId"
      :terminalType="terminalType"
      :isOpen="isTerminalOpen"
      @hide="isTerminalOpen = false"
      @close="isTerminalOpen = false"
    />
  </div>
</template>
