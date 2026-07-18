<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { AgentInfo } from "../types";

const props = defineProps<{
  agents: AgentInfo[];
  selectedAgentId: string;
  selectedDir: string;
  prompt: string;
  loading: boolean;
}>();

const emit = defineEmits<{
  (e: "update:selectedAgentId", val: string): void;
  (e: "update:selectedDir", val: string): void;
  (e: "update:prompt", val: string): void;
  (e: "submit"): void;
}>();

const localAgentId = computed({
  get: () => props.selectedAgentId,
  set: (val) => emit("update:selectedAgentId", val),
});

const baseDir = ref("");
const subDir = ref("");

const currentAgent = computed(() => {
  return props.agents.find((a) => a.id === props.selectedAgentId) || null;
});

const runDirs = computed(() => {
  return currentAgent.value?.run_dirs || [];
});

watch(
  [() => props.selectedDir, runDirs],
  ([newSelectedDir, newRunDirs]) => {
    let bestMatch = "";
    for (const dir of newRunDirs) {
      if (newSelectedDir.startsWith(dir) && dir.length > bestMatch.length) {
        bestMatch = dir;
      }
    }
    if (bestMatch) {
      baseDir.value = bestMatch;
      let remaining = newSelectedDir.slice(bestMatch.length);
      if (remaining.startsWith("/")) {
        remaining = remaining.slice(1);
      }
      subDir.value = remaining;
    } else {
      baseDir.value = newRunDirs[0] || "";
      subDir.value = "";
    }
  },
  { immediate: true },
);

watch([baseDir, subDir], () => {
  let combined = baseDir.value;
  if (subDir.value.trim()) {
    const sub = subDir.value.trim().replace(/^\/+/, "");
    combined = combined.endsWith("/") ? `${combined}${sub}` : `${combined}/${sub}`;
  }
  emit("update:selectedDir", combined);
});

const localPrompt = computed({
  get: () => props.prompt,
  set: (val) => emit("update:prompt", val),
});

const handleSubmit = () => {
  if (localPrompt.value.trim() && !props.loading) {
    emit("submit");
  }
};
</script>

<template>
  <div class="flex-1 flex flex-col justify-center items-center p-8 bg-base-100 overflow-y-auto">
    <div
      class="max-w-2xl w-full space-y-8 bg-base-200 p-8 rounded-2xl shadow-xl border border-base-300"
    >
      <!-- App title & intro -->
      <div class="text-center space-y-2">
        <h2
          class="text-3xl font-extrabold bg-gradient-to-r from-indigo-400 to-cyan-400 bg-clip-text text-transparent"
        >
          Start a Chat
        </h2>
        <p class="text-sm text-base-content/60">
          Select an agent, workspace directory, and start building.
        </p>
      </div>

      <div class="space-y-6">
        <!-- Agent Selection -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text">Select Coding Agent</span>
          </label>
          <select
            v-model="localAgentId"
            class="select select-bordered w-full bg-base-300 border-base-300 focus:outline-none"
          >
            <option v-for="agent in agents" :key="agent.id" :value="agent.id">
              {{ agent.name }} ({{ agent.id }})
            </option>
          </select>
          <label class="label text-xs text-base-content/50" v-if="currentAgent">
            <span>{{ currentAgent.description }}</span>
          </label>
        </div>

        <!-- Run Directory Selection -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text">Workspace (Run Directory)</span>
          </label>
          <select
            v-model="baseDir"
            class="select select-bordered w-full bg-base-300 border-base-300 focus:outline-none"
            :disabled="runDirs.length === 0"
          >
            <option v-for="dir in runDirs" :key="dir" :value="dir">
              {{ dir }}
            </option>
          </select>
          <label class="label text-xs text-warning" v-if="runDirs.length === 0">
            <span>No directories available for this agent</span>
          </label>
        </div>

        <!-- Subdirectory Selection -->
        <div class="form-control w-full" v-if="runDirs.length > 0">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text">Subdirectory (optional)</span>
          </label>
          <input
            v-model="subDir"
            type="text"
            placeholder="e.g. subdir/project"
            class="input input-bordered w-full bg-base-300 border-base-300 focus:outline-none text-sm"
          />
          <label class="label text-xs text-base-content/50" v-if="selectedDir">
            <span class="truncate">Full path: {{ selectedDir }}</span>
          </label>
        </div>

        <!-- Prompt Textarea -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text">What would you like to build?</span>
          </label>
          <textarea
            v-model="localPrompt"
            class="textarea textarea-bordered h-32 bg-base-300 border-base-300 w-full focus:outline-none font-mono text-sm leading-relaxed"
            placeholder="Type your coding request here..."
            @keydown.enter.exact.prevent="handleSubmit"
          ></textarea>
        </div>

        <!-- Start Button -->
        <button
          @click="handleSubmit"
          class="btn btn-primary w-full flex items-center justify-center gap-2"
          :disabled="!localPrompt.trim() || loading || !selectedDir"
        >
          <span v-if="loading" class="loading loading-spinner loading-xs"></span>
          <span>Start Agent Run</span>
        </button>
      </div>
    </div>
  </div>
</template>
