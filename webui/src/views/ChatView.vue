<script setup lang="ts">
import ChatArea from "../components/ChatArea.vue";
import ChatInput from "../components/ChatInput.vue";
import DiffView from "../components/DiffView.vue";
import type { ChatMessage, AgentInfo } from "../types";

defineProps<{
  messages: ChatMessage[];
  loading: boolean;
  activeAgent: AgentInfo | null;
  runDir: string;
  sessionId: string;
  showDiffView: boolean;
  gitRoot: string;
}>();

const isDetailsOpen = defineModel<boolean>("isDetailsOpen");
const chatInputText = defineModel<string>("chatInputText");

defineEmits<{
  (e: "send", text: string): void;
  (e: "open-diff", gitRoot: string): void;
  (e: "close-diff"): void;
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
  <ChatArea
    v-else
    :messages="messages"
    :loading="loading"
    :activeAgent="activeAgent"
    :runDir="runDir"
    :sessionId="sessionId"
    v-model:isDetailsOpen="isDetailsOpen"
    @open-diff="(g) => $emit('open-diff', g)"
  />
  <ChatInput
    v-if="!showDiffView"
    @send="(text) => $emit('send', text)"
    :loading="loading"
    v-model="chatInputText"
  />
</template>
