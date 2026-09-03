<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Icon } from "@iconify/vue";
import type { AgentInfo, VoiceErrorCode } from "../types";
import { getDirInfo, getSubdirs } from "../lib/api";
import { useShortcuts } from "../composables/useShortcuts";
import { useToast } from "../composables/useToast";
import { useVoiceInput } from "../composables/useVoiceInput";
import AttachmentChips from "./chat/AttachmentChips.vue";
import VoiceInputButton from "./chat/VoiceInputButton.vue";
import { t } from "../i18n";

const { toggleSidebarShortcut, sendShortcut } = useShortcuts();
const toast = useToast();

const props = defineProps<{
  agents: AgentInfo[];
  selectedAgentId: string;
  selectedDir: string;
  selectedModel?: string;
  prompt: string;
  loading: boolean;
}>();

const emit = defineEmits<{
  (e: "update:selectedAgentId", val: string): void;
  (e: "update:selectedDir", val: string): void;
  (e: "update:selectedModel", val: string): void;
  (e: "update:prompt", val: string): void;
  (e: "submit", files?: File[]): void;
  (e: "toggle-sidebar"): void;
}>();

const welcomeFiles = ref<File[]>([]);
const isDragging = ref(false);
const fileInputRef = ref<HTMLInputElement | null>(null);
let dragEnterCounter = 0;

const MAX_ATTACHMENTS = 20;
const MAX_SINGLE_FILE_SIZE = 20 * 1024 * 1024; // 20MB
const MAX_TOTAL_FILES_SIZE = 50 * 1024 * 1024; // 50MB

const addFiles = (files: FileList | File[]) => {
  const fileArray = Array.from(files);
  if (fileArray.length === 0) return;

  if (welcomeFiles.value.length + fileArray.length > MAX_ATTACHMENTS) {
    toast.error(t("chat.maxAttachmentsExceeded", { max: MAX_ATTACHMENTS }));
    return;
  }

  let currentTotalSize = welcomeFiles.value.reduce((sum, f) => sum + f.size, 0);

  for (const file of fileArray) {
    if (file.size > MAX_SINGLE_FILE_SIZE) {
      toast.error(t("chat.fileSizeExceeded", { name: file.name }));
      return;
    }
    if (currentTotalSize + file.size > MAX_TOTAL_FILES_SIZE) {
      toast.error(t("chat.totalSizeExceeded"));
      return;
    }
    // Avoid duplicate files (same name & size)
    const isDuplicate = welcomeFiles.value.some(
      (existing) => existing.name === file.name && existing.size === file.size,
    );
    if (!isDuplicate) {
      welcomeFiles.value.push(file);
      currentTotalSize += file.size;
    }
  }
};

const removeFile = (index: number) => {
  welcomeFiles.value.splice(index, 1);
};

const handleFileChange = (e: Event) => {
  const target = e.target as HTMLInputElement;
  if (target.files && target.files.length > 0) {
    addFiles(target.files);
  }
  if (fileInputRef.value) {
    fileInputRef.value.value = "";
  }
};

const handlePaste = (e: ClipboardEvent) => {
  if (props.loading) return;
  const files = e.clipboardData?.files;
  if (files && files.length > 0) {
    e.preventDefault();
    addFiles(files);
  }
};

const handleDragEnter = (e: DragEvent) => {
  if (props.loading) return;
  if (e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files")) {
    dragEnterCounter++;
    isDragging.value = true;
  }
};

const handleDragOver = (e: DragEvent) => {
  if (props.loading) return;
  if (e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files")) {
    e.preventDefault();
  }
};

const handleDragLeave = (_e: DragEvent) => {
  if (props.loading) return;
  dragEnterCounter--;
  if (dragEnterCounter <= 0) {
    dragEnterCounter = 0;
    isDragging.value = false;
  }
};

const handleDrop = (e: DragEvent) => {
  dragEnterCounter = 0;
  isDragging.value = false;
  if (props.loading) return;
  e.preventDefault();
  if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
    addFiles(e.dataTransfer.files);
  }
};

const triggerFileInput = () => {
  if (props.loading) return;
  fileInputRef.value?.click();
};

const localModel = computed({
  get: () => props.selectedModel || "",
  set: (val) => emit("update:selectedModel", val),
});

const localAgentId = computed({
  get: () => props.selectedAgentId,
  set: (val) => emit("update:selectedAgentId", val),
});

const baseDir = ref("");
const subSegments = ref<string[]>([]);
const levelSubdirs = ref<string[][]>([]);
const loadingLevels = ref<boolean[]>([]);
const selectedGitRoot = ref("");

const mainAgents = computed(() => {
  return props.agents.filter((a) => a.main_agent !== false);
});

const agentDisplayName = (agent: AgentInfo): string => {
  const prefix = agent.type === "workflow" ? "[Workflow] " : "";
  return `${prefix}${agent.name} (${agent.id})`;
};

const currentAgent = computed(() => {
  return props.agents.find((a) => a.id === props.selectedAgentId) || null;
});

const currentAgentModels = computed(() => {
  return currentAgent.value?.models || [];
});

const runDirs = computed(() => {
  return currentAgent.value?.run_dirs || [];
});

const baseFolderName = computed(() => {
  if (!baseDir.value) return "root";
  const parts = baseDir.value.split("/").filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : baseDir.value;
});

const computePath = (base: string, segments: string[]) => {
  let combined = base;
  if (segments.length > 0) {
    const sub = segments.join("/");
    combined = combined.endsWith("/") ? `${combined}${sub}` : `${combined}/${sub}`;
  }
  return combined;
};

watch(
  () => props.selectedDir,
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

const loadSubdirsForLevel = async (currentBase: string, segments: string[], levelIndex: number) => {
  const currentPath = [currentBase, ...segments.slice(0, levelIndex)]
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

const loadSubdirsUntil = async (currentBase: string, targetSegments: string[]) => {
  const newLevelSubdirs: string[][] = [];
  const newLoadingLevels: boolean[] = [];

  for (let i = 0; i <= targetSegments.length; i++) {
    const currentPath = [currentBase, ...targetSegments.slice(0, i)].join("/").replace(/\/+/g, "/");
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

// Sync internal cascader state when props.selectedDir or runDirs change from outside
watch(
  [() => props.selectedDir, runDirs],
  async ([newSelectedDir, newRunDirs]) => {
    // Skip redundant fetch if external props already match current internal cascader path
    if (baseDir.value && newSelectedDir === computePath(baseDir.value, subSegments.value)) {
      return;
    }

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
      await loadSubdirsUntil(bestMatch, segments);
    } else {
      baseDir.value = newRunDirs[0] || "";
      subSegments.value = [];
      if (baseDir.value) {
        await loadSubdirsForLevel(baseDir.value, [], 0);
      } else {
        levelSubdirs.value = [];
      }
    }
  },
  { immediate: true },
);

const handleBaseDirChange = async (event: Event) => {
  const newBase = (event.target as HTMLSelectElement).value;
  baseDir.value = newBase;
  subSegments.value = [];
  levelSubdirs.value = [];
  emit("update:selectedDir", newBase);
  if (newBase) {
    await loadSubdirsForLevel(newBase, [], 0);
  }
};

const handleSelectSegment = async (levelIndex: number, event: Event) => {
  const val = (event.target as HTMLSelectElement).value;
  let newSegments: string[];
  if (!val) {
    newSegments = subSegments.value.slice(0, levelIndex);
    subSegments.value = newSegments;
    levelSubdirs.value = levelSubdirs.value.slice(0, levelIndex + 1);
  } else {
    newSegments = [...subSegments.value.slice(0, levelIndex), val];
    subSegments.value = newSegments;
    levelSubdirs.value = levelSubdirs.value.slice(0, levelIndex + 1);
    await loadSubdirsForLevel(baseDir.value, newSegments, levelIndex + 1);
  }
  emit("update:selectedDir", computePath(baseDir.value, newSegments));
};

const resetSubSegments = () => {
  subSegments.value = [];
  levelSubdirs.value = levelSubdirs.value.slice(0, 1);
  emit("update:selectedDir", baseDir.value);
};

const localPrompt = computed({
  get: () => props.prompt,
  set: (val) => emit("update:prompt", val),
});

const VOICE_ERROR_I18N_MAP: Record<VoiceErrorCode, string> = {
  micDenied: "chat.micPermissionDenied",
  voiceUnavailable: "chat.voiceUnavailable",
  sessionTimeout: "chat.voiceSessionTimeout",
  network: "chat.voiceUnavailable",
};

const handleVoiceFinalText = (finalText: string) => {
  const trimmed = finalText.trim();
  if (!trimmed) return;
  if (!localPrompt.value) {
    localPrompt.value = trimmed;
  } else if (/\s$/.test(localPrompt.value)) {
    localPrompt.value += trimmed;
  } else {
    localPrompt.value += " " + trimmed;
  }
};

const {
  isRecording,
  isConnecting,
  isStopping,
  interimText,
  livePreviewText,
  error: voiceError,
  startRecording,
  stopRecording,
  cancelRecording,
} = useVoiceInput({
  onFinalText: handleVoiceFinalText,
});

watch(voiceError, (code) => {
  if (code) {
    const key = VOICE_ERROR_I18N_MAP[code] || "chat.voiceUnavailable";
    toast.error(t(key));
  }
});

const handleVoiceToggle = () => {
  if (isRecording.value || isConnecting.value) {
    void stopRecording();
  } else {
    void startRecording();
  }
};

onBeforeUnmount(() => {
  cancelRecording();
});

const handleSubmit = () => {
  if (isRecording.value) {
    cancelRecording();
  }
  if (localPrompt.value.trim() && !props.loading) {
    const files = welcomeFiles.value.length > 0 ? [...welcomeFiles.value] : undefined;
    emit("submit", files);
    welcomeFiles.value = [];
  }
};
</script>

<template>
  <div class="flex-1 flex flex-col h-full bg-base-100 overflow-hidden relative">
    <!-- Top Header -->
    <header
      class="px-3 py-2 sm:px-6 sm:py-3 bg-base-200 border-b border-base-300 flex items-center justify-between shadow-sm shrink-0 min-w-0"
    >
      <div class="flex items-center gap-2 min-w-0">
        <button
          @click="emit('toggle-sidebar')"
          class="md:hidden btn btn-ghost btn-xs btn-square text-base-content/80 shrink-0"
          :title="$t('chat.toggleMenu', { shortcut: toggleSidebarShortcut })"
        >
          <Icon icon="mynaui:sidebar" class="h-5 w-5" />
        </button>
        <span class="text-sm font-bold text-base-content flex items-center gap-2">
          <Icon icon="mynaui:edit-one" class="h-5 w-5 text-primary" />
          <span>{{ $t("chat.newChat") }}</span>
        </span>
      </div>
    </header>

    <div
      class="flex-1 flex flex-col justify-center items-center p-3 sm:p-8 bg-base-100 overflow-y-auto"
    >
      <div
        class="max-w-2xl w-full space-y-6 sm:space-y-8 bg-base-200 p-4 sm:p-8 rounded-2xl shadow-xl border border-base-300 transition-all"
        @dragenter="handleDragEnter"
        @dragover="handleDragOver"
        @dragleave="handleDragLeave"
        @drop="handleDrop"
        :class="{ 'ring-2 ring-primary ring-inset': isDragging }"
      >
        <!-- Hidden file input -->
        <input ref="fileInputRef" type="file" multiple class="hidden" @change="handleFileChange" />

        <!-- App title & intro -->
        <div class="text-center space-y-1.5 sm:space-y-2">
          <h2
            class="text-2xl sm:text-3xl font-extrabold bg-gradient-to-r from-indigo-600 to-cyan-600 dark:from-indigo-400 dark:to-cyan-400 bg-clip-text text-transparent"
          >
            {{ $t("chat.startAChat") }}
          </h2>
          <p class="text-xs sm:text-sm text-base-content/60">
            {{ $t("chat.startSubtitle") }}
          </p>
        </div>

        <!-- Agent Selection -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">{{ $t("chat.selectAgent") }}</span>
          </label>
          <select
            v-model="localAgentId"
            class="select select-bordered w-full bg-base-100 border-base-300 text-base-content focus:outline-none"
          >
            <option
              v-for="agent in mainAgents"
              :key="agent.id"
              :value="agent.id"
              class="bg-base-100 text-base-content"
            >
              {{ agentDisplayName(agent) }}
            </option>
          </select>
          <p
            class="text-xs text-base-content/60 mt-1.5 leading-relaxed break-words"
            v-if="currentAgent?.description"
          >
            {{ currentAgent.description }}
          </p>
        </div>

        <!-- Model Selection (Optional) -->
        <div class="form-control w-full" v-if="currentAgentModels.length > 0">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">{{ $t("chat.selectModelOptional") }}</span>
          </label>
          <select
            v-model="localModel"
            class="select select-bordered w-full bg-base-100 border-base-300 text-base-content focus:outline-none disabled:bg-base-200 disabled:opacity-60"
          >
            <option value="" class="bg-base-100 text-base-content">
              {{ $t("chat.autoModelFallback") }}
            </option>
            <option
              v-for="m in currentAgentModels"
              :key="m"
              :value="m"
              class="bg-base-100 text-base-content"
            >
              {{ m }}
            </option>
          </select>
          <p class="text-xs text-base-content/60 mt-1.5 leading-relaxed break-words">
            {{ $t("chat.modelFallbackNotice") }}
          </p>
        </div>

        <!-- Run Directory Selection -->
        <div class="form-control w-full">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">{{ $t("chat.workspaceRunDir") }}</span>
          </label>
          <select
            :value="baseDir"
            @change="handleBaseDirChange"
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
          <p class="text-xs text-warning mt-1.5 break-words" v-if="runDirs.length === 0">
            {{ $t("chat.noDirsAvailable") }}
          </p>
        </div>

        <!-- Subdirectory Selection (Theme-Aware Unified Cascader Row) -->
        <div class="form-control w-full" v-if="runDirs.length > 0">
          <label class="label font-semibold text-sm text-base-content/85">
            <span class="label-text text-base-content">{{ $t("chat.subdirectoryOptional") }}</span>
          </label>

          <div class="p-3.5 bg-base-100 rounded-xl border border-base-300 shadow-sm space-y-3">
            <!-- Full Path Bar & Reset Action -->
            <div
              class="flex flex-col gap-1.5 text-xs text-base-content/70 pb-2 border-b border-base-300/60 font-mono"
            >
              <div class="flex items-center justify-between min-w-0">
                <div class="flex items-center gap-1.5 min-w-0 pr-2">
                  <span class="text-primary font-semibold shrink-0">{{ $t("chat.fullPath") }}</span>
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
                  {{ $t("chat.reset") }}
                </button>
              </div>
              <div v-if="selectedGitRoot" class="flex items-center gap-1.5 min-w-0 pr-2">
                <span class="text-primary font-semibold shrink-0">{{ $t("chat.gitRoot") }}</span>
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
                      <option value="">{{ $t("chat.stopHere") }}</option>
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
                    :title="$t('chat.endOfFolders')"
                  >
                    <Icon icon="mdi:dollar" class="w-4 h-4 text-base-content/60" />
                  </div>
                </template>
              </template>
            </div>
          </div>
        </div>

        <!-- Prompt Textarea & Attachments -->
        <div class="form-control w-full space-y-2">
          <div class="flex items-center justify-between">
            <label class="label p-0 font-semibold text-sm text-base-content/85">
              <span class="label-text text-base-content">{{ $t("chat.promptLabel") }}</span>
            </label>
            <div class="flex items-center gap-1">
              <!-- Voice Input Button -->
              <VoiceInputButton
                :is-recording="isRecording"
                :is-connecting="isConnecting"
                :is-stopping="isStopping"
                :disabled="loading"
                @toggle="handleVoiceToggle"
              />
              <!-- Attach Files Button -->
              <button
                type="button"
                @click="triggerFileInput"
                :disabled="loading"
                class="btn btn-ghost btn-xs gap-1 text-base-content/70 hover:text-base-content"
                :title="$t('chat.attachFiles')"
              >
                <Icon icon="material-symbols:attach-file" class="h-4 w-4" />
                <span>{{ $t("chat.attachFiles") }}</span>
              </button>
            </div>
          </div>

          <!-- Live Interim Transcript Preview -->
          <div
            v-if="isRecording"
            class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-base-300 text-base-content text-xs border border-base-content/10 shadow-sm animate-pulse"
          >
            <span class="loading loading-ring loading-xs text-primary shrink-0"></span>
            <span class="font-medium text-primary shrink-0">{{ $t("chat.recording") }}</span>
            <span class="truncate italic opacity-85">
              {{ livePreviewText || interimText || $t("chat.voiceInterimPlaceholder") }}
            </span>
          </div>

          <!-- Attachment Chips List -->
          <AttachmentChips :attachments="welcomeFiles" @remove="removeFile" />

          <textarea
            v-model="localPrompt"
            class="textarea textarea-bordered h-32 bg-base-100 border-base-300 text-base-content w-full focus:outline-none font-mono text-sm leading-relaxed"
            :placeholder="$t('chat.promptPlaceholder', { shortcut: sendShortcut })"
            @keydown.ctrl.enter.prevent="handleSubmit"
            @keydown.meta.enter.prevent="handleSubmit"
            @paste="handlePaste"
          ></textarea>
        </div>

        <!-- Start Button -->
        <button
          @click="handleSubmit"
          class="btn btn-primary w-full flex items-center justify-center gap-2"
          :disabled="!localPrompt.trim() || loading || !selectedDir"
        >
          <span v-if="loading" class="loading loading-spinner loading-xs"></span>
          <span>{{
            currentAgent?.type === "workflow"
              ? $t("chat.startWorkflowRun")
              : $t("chat.startAgentRun")
          }}</span>
        </button>
      </div>
    </div>
  </div>
</template>
