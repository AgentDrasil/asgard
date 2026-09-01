<script setup lang="ts">
import WelcomeScreen from "../components/WelcomeScreen.vue";
import type { AgentInfo } from "../types";

defineProps<{
  agents: AgentInfo[];
  loading: boolean;
}>();

const selectedAgentId = defineModel<string>("selectedAgentId", { required: true });
const selectedDir = defineModel<string>("selectedDir", { required: true });
const selectedModel = defineModel<string>("selectedModel");
const prompt = defineModel<string>("prompt", { required: true });

defineEmits<{
  (e: "submit", files?: File[]): void;
  (e: "toggle-sidebar"): void;
}>();
</script>

<template>
  <WelcomeScreen
    :agents="agents"
    v-model:selectedAgentId="selectedAgentId"
    v-model:selectedDir="selectedDir"
    v-model:selectedModel="selectedModel"
    v-model:prompt="prompt"
    @submit="(files) => $emit('submit', files)"
    @toggle-sidebar="$emit('toggle-sidebar')"
    :loading="loading"
  />
</template>
