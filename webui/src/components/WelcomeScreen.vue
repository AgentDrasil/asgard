<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Icon } from "@iconify/vue";
import type { AgentInfo } from "../types";
import { getDirInfo, getSubdirs } from "../lib/api";

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
const subSegments = ref<string[]>([]);
const levelSubdirs = ref<string[][]>([]);
const loadingLevels = ref<boolean[]>([]);
const isSyncingFromProps = ref(false);
const selectedGitRoot = ref("");

const currentAgent = computed(() => {
  return props.agents.find((a) => a.id === props.selectedAgentId) || null;
});

const runDirs = computed(() => {
  return currentAgent.value?.run_dirs || [];
});

const baseFolderName = computed(() => {
  if (!baseDir.value) return "root";
  const parts = baseDir.value.split("/").filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : baseDir.value;
});

const computedSelectedDir = computed(() => {
  let combined = baseDir.value;
  if (subSegments.value.length > 0) {
    const sub = subSegments.value.join("/");
    combined = combined.endsWith("/") ? `${combined}${sub}` : `${combined}/${sub}`;
  }
  return combined;
});

watch(
  computedSelectedDir,
  async (newDir) => {
    if (!newDir) {
      selectedGitRoot.value = "";
      return;
    }
    const info = await getDirInfo(newDir);
    selectedGitRoot.value = info.gitRoot || "";
  },
  { immediate: true },
);

const loadSubdirsForLevel = async (levelIndex: number) => {
  const currentPath = [baseDir.value, ...subSegments.value.slice(0, levelIndex)]
    .join("/")
    .replace(/\/+/g, "/");

  const newLoading = [...loadingLevels.value];
  newLoading[levelIndex] = true;
  loadingLevels.value = newLoading;

  const subdirs = await getSubdirs(currentPath);

  const newLevels = [...levelSubdirs.value];
  newLevels[levelIndex] = subdirs;
  levelSubdirs.value = newLevels;

  const doneLoading = [...loadingLevels.value];
  doneLoading[levelIndex] = false;
  loadingLevels.value = doneLoading;
};

const loadSubdirsUntil = async (targetSegments: string[]) => {
  const newLevelSubdirs: string[][] = [];
  const newLoadingLevels: boolean[] = [];

  for (let i = 0; i <= targetSegments.length; i++) {
    const currentPath = [baseDir.value, ...targetSegments.slice(0, i)]
      .join("/")
      .replace(/\/+/g, "/");
    newLoadingLevels[i] = true;
    loadingLevels.value = [...newLoadingLevels];
    const subdirs = await getSubdirs(currentPath);
    newLevelSubdirs[i] = subdirs;
    newLoadingLevels[i] = false;
    loadingLevels.value = [...newLoadingLevels];
    if (subdirs.length === 0) {
      break;
    }
  }

  levelSubdirs.value = newLevelSubdirs;
};

watch(
  [() => props.selectedDir, runDirs],
  async ([newSelectedDir, newRunDirs]) => {
    if (isSyncingFromProps.value) return;
    isSyncingFromProps.value = true;
    try {
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
        const segments = remaining ? remaining.split("/").filter(Boolean) : [];
        subSegments.value = segments;
        await loadSubdirsUntil(segments);
      } else {
        baseDir.value = newRunDirs[0] || "";
        subSegments.value = [];
        if (baseDir.value) {
          await loadSubdirsForLevel(0);
        } else {
          levelSubdirs.value = [];
        }
      }
    } finally {
      isSyncingFromProps.value = false;
    }
  },
  { immediate: true },
);

watch(baseDir, async (newBaseDir, oldBaseDir) => {
  if (isSyncingFromProps.value || newBaseDir === oldBaseDir) return;
  subSegments.value = [];
  levelSubdirs.value = [];
  if (newBaseDir) {
    await loadSubdirsForLevel(0);
  }
});

watch(computedSelectedDir, (newDir) => {
  if (!isSyncingFromProps.value) {
    emit("update:selectedDir", newDir);
  }
});

const handleSelectSegment = async (levelIndex: number, event: Event) => {
  const val = (event.target as HTMLSelectElement).value;
  if (!val) {
    subSegments.value = subSegments.value.slice(0, levelIndex);
    levelSubdirs.value = levelSubdirs.value.slice(0, levelIndex + 1);
  } else {
    subSegments.value = [...subSegments.value.slice(0, levelIndex), val];
    levelSubdirs.value = levelSubdirs.value.slice(0, levelIndex + 1);
    await loadSubdirsForLevel(levelIndex + 1);
  }
};

const resetSubSegments = () => {
  subSegments.value = [];
  levelSubdirs.value = levelSubdirs.value.slice(0, 1);
};

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
  <div
    class="flex-1 flex flex-col justify-center items-center p-3 sm:p-8 bg-base-100 overflow-y-auto"
  >
    <div
      class="max-w-2xl w-full space-y-6 sm:space-y-8 bg-base-200 p-4 sm:p-8 rounded-2xl shadow-xl border border-base-300"
    >
      <!-- App title & intro -->
      <div class="text-center space-y-1.5 sm:space-y-2">
        <h2
          class="text-2xl sm:text-3xl font-extrabold bg-gradient-to-r from-indigo-600 to-cyan-600 dark:from-indigo-400 dark:to-cyan-400 bg-clip-text text-transparent"
        >
          Start a Chat
        </h2>
        <p class="text-xs sm:text-sm text-base-content/60">
          Select an agent, workspace directory, and start building.
        </p>
      </div>

      <div class="space-y-6">
        <!-- Agent Selection -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">Select Coding Agent</span>
          </label>
          <select
            v-model="localAgentId"
            class="select select-bordered w-full bg-base-100 border-base-300 text-base-content focus:outline-none"
          >
            <option
              v-for="agent in agents"
              :key="agent.id"
              :value="agent.id"
              class="bg-base-100 text-base-content"
            >
              {{ agent.name }} ({{ agent.id }})
            </option>
          </select>
          <label class="label text-xs text-base-content/60" v-if="currentAgent">
            <span>{{ currentAgent.description }}</span>
          </label>
        </div>

        <!-- Run Directory Selection -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">Workspace (Run Directory)</span>
          </label>
          <select
            v-model="baseDir"
            class="select select-bordered w-full bg-base-100 border-base-300 text-base-content focus:outline-none"
            :disabled="runDirs.length === 0"
          >
            <option
              v-for="dir in runDirs"
              :key="dir"
              :value="dir"
              class="bg-base-100 text-base-content"
            >
              {{ dir }}
            </option>
          </select>
          <label class="label text-xs text-warning" v-if="runDirs.length === 0">
            <span>No directories available for this agent</span>
          </label>
        </div>

        <!-- Subdirectory Selection (Theme-Aware Unified Cascader Row) -->
        <div class="form-control w-full" v-if="runDirs.length > 0">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">Subdirectory (optional)</span>
          </label>

          <div class="p-3.5 bg-base-100 rounded-xl border border-base-300 shadow-sm space-y-3">
            <!-- Full Path Bar & Reset Action -->
            <div
              class="flex flex-col gap-1.5 text-xs text-base-content/70 pb-2 border-b border-base-300/60 font-mono"
            >
              <div class="flex items-center justify-between min-w-0">
                <div class="flex items-center gap-1.5 min-w-0 pr-2">
                  <span class="text-primary font-semibold shrink-0">Full Path:</span>
                  <span
                    class="truncate bg-base-200 px-2 py-0.5 rounded text-base-content font-semibold border border-base-300/40"
                  >
                    {{ selectedDir }}
                  </span>
                </div>
                <button
                  v-if="subSegments.length > 0"
                  type="button"
                  @click="resetSubSegments"
                  class="btn btn-ghost btn-xs text-error/80 hover:text-error shrink-0"
                >
                  Reset
                </button>
              </div>
              <div v-if="selectedGitRoot" class="flex items-center gap-1.5 min-w-0 pr-2">
                <span class="text-primary font-semibold shrink-0">Git Root:</span>
                <span
                  class="truncate bg-base-200 px-2 py-0.5 rounded text-base-content font-semibold border border-base-300/40"
                >
                  {{ selectedGitRoot }}
                </span>
              </div>
            </div>

            <!-- Inline Cascader Selector Row -->
            <div class="flex flex-wrap items-center gap-2">
              <!-- Workspace Base Folder Pill -->
              <div
                class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg bg-base-200 border border-base-300 text-base-content font-mono text-xs font-bold shrink-0"
              >
                <Icon
                  icon="material-symbols-light:home-rounded"
                  class="w-4 h-4 text-primary shrink-0"
                />
                <span>{{ baseFolderName }}</span>
              </div>

              <!-- Level Dropdowns -->
              <template v-for="(subdirs, levelIndex) in levelSubdirs" :key="levelIndex">
                <template v-if="subdirs.length > 0 || levelIndex < subSegments.length">
                  <span class="text-base-content/40 font-bold select-none text-xs">/</span>

                  <div class="relative flex items-center">
                    <select
                      :value="subSegments[levelIndex] || ''"
                      @change="(e) => handleSelectSegment(levelIndex, e)"
                      class="select select-bordered select-sm h-8 min-h-0 py-0 pl-2.5 pr-7 rounded-lg bg-base-100 border-base-300 font-mono text-xs text-base-content focus:outline-none max-w-48"
                      :disabled="loadingLevels[levelIndex]"
                    >
                      <option value="">-- Stop here --</option>
                      <option v-for="dir in subdirs" :key="dir" :value="dir">{{ dir }}/</option>
                    </select>

                    <span
                      v-if="loadingLevels[levelIndex]"
                      class="loading loading-spinner loading-xs text-primary absolute right-7 pointer-events-none"
                    ></span>
                  </div>
                </template>

                <template
                  v-else-if="
                    levelIndex === subSegments.length &&
                    subSegments.length > 0 &&
                    subdirs.length === 0
                  "
                >
                  <span class="text-base-content/40 font-bold select-none text-xs">/</span>
                  <div
                    class="h-8 inline-flex items-center justify-center px-3 rounded-lg bg-base-200/60 border border-base-300/50 text-base-content/50 font-mono text-xs shrink-0"
                    title="End of folders"
                  >
                    <Icon icon="mdi:dollar" class="w-4 h-4 text-base-content/60" />
                  </div>
                </template>
              </template>
            </div>
          </div>
        </div>

        <!-- Prompt Textarea -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">What would you like to build?</span>
          </label>
          <textarea
            v-model="localPrompt"
            class="textarea textarea-bordered h-32 bg-base-100 border-base-300 text-base-content w-full focus:outline-none font-mono text-sm leading-relaxed"
            placeholder="Type your coding request here... (Ctrl+Enter to submit)"
            @keydown.ctrl.enter.prevent="handleSubmit"
            @keydown.meta.enter.prevent="handleSubmit"
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
