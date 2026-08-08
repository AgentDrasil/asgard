<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from "vue";
import ChatArea from "../components/ChatArea.vue";
import ChatInput from "../components/ChatInput.vue";
import DiffView from "../components/DiffView.vue";
import TerminalPanel from "../components/TerminalPanel.vue";
import ArtifactViewer from "../components/ArtifactViewer.vue";
import type { ChatMessage, AgentInfo } from "../types";

const props = defineProps<{
  messages: ChatMessage[];
  artifacts?: string[];
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

// Artifacts state management
const activeArtifactPath = ref<string | null>(null);
const modifiedFiles = ref<string[]>([]);
const isArtifactDrawerOpen = ref<boolean>(false);

// Resizable artifact panel width logic
const DEFAULT_ARTIFACT_WIDTH = 500;
const MIN_ARTIFACT_WIDTH = 300;
const MAX_ARTIFACT_WIDTH = 900;

const artifactWidth = ref(DEFAULT_ARTIFACT_WIDTH);
const isResizingArtifact = ref(false);
const isDesktop = ref(typeof window !== "undefined" && window.innerWidth >= 768);

const updateWindowWidth = () => {
  isDesktop.value = window.innerWidth >= 768;
};

const startArtifactResize = (e: MouseEvent) => {
  e.preventDefault();
  isResizingArtifact.value = true;
  document.addEventListener("mousemove", handleArtifactMouseMove);
  document.addEventListener("mouseup", stopArtifactResize);
  document.body.style.userSelect = "none";
  document.body.style.cursor = "col-resize";
};

const handleArtifactMouseMove = (e: MouseEvent) => {
  if (!isResizingArtifact.value) return;
  const newWidth = Math.min(
    Math.max(window.innerWidth - e.clientX, MIN_ARTIFACT_WIDTH),
    MAX_ARTIFACT_WIDTH,
  );
  artifactWidth.value = newWidth;
};

const stopArtifactResize = () => {
  if (isResizingArtifact.value) {
    isResizingArtifact.value = false;
    localStorage.setItem("asgard_artifact_width", artifactWidth.value.toString());
    document.removeEventListener("mousemove", handleArtifactMouseMove);
    document.removeEventListener("mouseup", stopArtifactResize);
    document.body.style.userSelect = "";
    document.body.style.cursor = "";
  }
};

onMounted(() => {
  window.addEventListener("resize", updateWindowWidth);
  const savedWidth = localStorage.getItem("asgard_artifact_width");
  if (savedWidth) {
    const parsed = parseInt(savedWidth, 10);
    if (!isNaN(parsed) && parsed >= MIN_ARTIFACT_WIDTH && parsed <= MAX_ARTIFACT_WIDTH) {
      artifactWidth.value = parsed;
    }
  }
});

onUnmounted(() => {
  window.removeEventListener("resize", updateWindowWidth);
});

const emit = defineEmits<{
  (e: "send", text: string): void;
  (e: "open-diff", gitRoot: string): void;
  (e: "close-diff"): void;
  (e: "toggle-terminal"): void;
  (e: "toggle-sidebar"): void;
}>();

// Helper to collect all artifact files from props.artifacts and props.messages
function syncArtifactFiles() {
  const currentFiles: string[] = [];

  // 1. Collect artifacts from session DB model
  if (props.artifacts && props.artifacts.length > 0) {
    for (const art of props.artifacts) {
      if (art && !currentFiles.includes(art)) {
        currentFiles.push(art);
      }
    }
  }

  // 2. Collect artifacts from message targetFiles
  if (props.messages && props.messages.length > 0) {
    for (const msg of props.messages) {
      if (msg.targetFiles && msg.targetFiles.length > 0) {
        for (const f of msg.targetFiles) {
          if (f && !currentFiles.includes(f)) {
            currentFiles.push(f);
          }
        }
      }
    }
  }

  modifiedFiles.value = currentFiles;

  // Keep activeArtifactPath valid for current session's files
  if (!activeArtifactPath.value || !modifiedFiles.value.includes(activeArtifactPath.value)) {
    activeArtifactPath.value =
      modifiedFiles.value.length > 0 ? modifiedFiles.value[modifiedFiles.value.length - 1] : null;
  }
}

// Reset artifact states on session switch
watch(
  () => props.sessionId,
  () => {
    modifiedFiles.value = [];
    activeArtifactPath.value = null;
    isArtifactDrawerOpen.value = false;
    syncArtifactFiles();
  },
  { immediate: true },
);

// Collect modified files from messages & session artifacts
watch(
  [() => props.messages, () => props.artifacts],
  () => {
    syncArtifactFiles();
  },
  { deep: true, immediate: true },
);

function handleOpenArtifact(file: string) {
  if (!modifiedFiles.value.includes(file)) {
    modifiedFiles.value.push(file);
  }
  activeArtifactPath.value = file;
  isArtifactDrawerOpen.value = true;
}

function toggleArtifactDrawer() {
  if (!isArtifactDrawerOpen.value) {
    if (!activeArtifactPath.value && modifiedFiles.value.length > 0) {
      activeArtifactPath.value = modifiedFiles.value[modifiedFiles.value.length - 1];
    }
    isArtifactDrawerOpen.value = true;
  } else {
    isArtifactDrawerOpen.value = false;
  }
}
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden relative">
    <!-- Top Area: Split ChatArea / DiffView & ArtifactViewer -->
    <div class="flex-1 flex h-full overflow-hidden relative min-h-0">
      <!-- Left: Chat Area (Messages) or DiffView -->
      <div
        class="flex-1 flex flex-col h-full overflow-hidden relative min-w-0"
        :class="isArtifactDrawerOpen && activeArtifactPath ? 'hidden md:flex' : 'flex'"
      >
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
          :isTerminalOpen="isTerminalOpen"
          :modifiedFiles="modifiedFiles"
          :isArtifactDrawerOpen="isArtifactDrawerOpen"
          v-model:isDetailsOpen="isDetailsOpen"
          @open-diff="(g) => $emit('open-diff', g)"
          @open-artifact="handleOpenArtifact"
          @toggle-terminal="emit('toggle-terminal')"
          @toggle-sidebar="emit('toggle-sidebar')"
          @toggle-artifact-drawer="toggleArtifactDrawer"
        />
      </div>

      <!-- Right: Resizable Artifact Panel -->
      <div
        v-if="isArtifactDrawerOpen && activeArtifactPath"
        class="w-full md:w-auto h-full shadow-2xl z-20 md:z-auto flex relative shrink-0"
        :class="{
          'transition-none': isResizingArtifact,
          'transition-[width] duration-200': !isResizingArtifact,
        }"
        :style="{
          width: isDesktop ? `${artifactWidth}px` : '100%',
        }"
      >
        <!-- Resizer Handle on Left Edge of Artifact Panel -->
        <div
          @mousedown="startArtifactResize"
          class="hidden md:block absolute top-0 left-0 w-1.5 h-full cursor-col-resize hover:bg-primary/50 transition-colors z-30"
          title="Drag to resize panel"
        ></div>

        <ArtifactViewer
          :sessionId="sessionId"
          :activeFilePath="activeArtifactPath"
          :modifiedFiles="modifiedFiles"
          @close="isArtifactDrawerOpen = false"
          @select-file="(f) => (activeArtifactPath = f)"
        />
      </div>
    </div>

    <!-- Bottom Area: ChatInput & TerminalPanel (Always Full Width across entire window) -->
    <div class="w-full shrink-0 flex flex-col z-20">
      <ChatInput @send="(text) => $emit('send', text)" :loading="loading" v-model="chatInputText" />
      <TerminalPanel
        :sessionId="sessionId"
        :terminalType="terminalType"
        :isOpen="isTerminalOpen"
        @hide="isTerminalOpen = false"
        @close="isTerminalOpen = false"
      />
    </div>
  </div>
</template>
