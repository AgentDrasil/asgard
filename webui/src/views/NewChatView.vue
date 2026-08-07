<script setup lang="ts">
import WelcomeScreen from "../components/WelcomeScreen.vue";
import type { AgentInfo } from "../types";

defineProps<{
  agents: AgentInfo[];
  loading: boolean;
}>();

const selectedAgentId = defineModel<string>("selectedAgentId");
const selectedDir = defineModel<string>("selectedDir");
const selectedModel = defineModel<string>("selectedModel");
const prompt = defineModel<string>("prompt");

defineEmits<{
  (e: "submit"): void;
}>();
</script>

<template>
  <WelcomeScreen
    :agents="agents"
    v-model:selectedAgentId="selectedAgentId"
    v-model:selectedDir="selectedDir"
    v-model:selectedModel="selectedModel"
    v-model:prompt="prompt"
    @submit="$emit('submit')"
    :loading="loading"
  />
</template>
