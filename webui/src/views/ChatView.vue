<script setup lang="ts">
import { ref, watch } from "vue";
import { useRouter } from "vue-router";
import ChatArea from "../components/ChatArea.vue";
import ChatInput from "../components/ChatInput.vue";
import WorkflowControlPanel from "../components/chat/WorkflowControlPanel.vue";
import DiffView from "../components/DiffView.vue";
import FileView from "../components/file/FileView.vue";
import TerminalPanel from "../components/TerminalPanel.vue";
import type { ChatMessage, AgentInfo, ActiveView } from "../types";
import { buildChatRoute, buildFilesRoute, buildVcsRoute } from "../utils/routeUtils";

const router = useRouter();

const props = defineProps<{
  messages: ChatMessage[];
  artifacts?: string[];
  loading: boolean;
  activeAgent: AgentInfo | null;
  agents?: AgentInfo[];
  runDir: string;
  sessionId: string;
  gitRoot: string;
  terminalType?: "session" | "sidebar";
  workingAgentLabel?: string | null;
}>();

const activeView = defineModel<ActiveView>("activeView", { default: "chat" });
const selectedFilePath = defineModel<string | null>("selectedFilePath", { default: null });
const selectedCommit = defineModel<string | null>("selectedCommit", { default: null });
const isFileTreeOpen = defineModel<boolean>("isFileTreeOpen", { default: true });
const isDetailsOpen = defineModel<boolean>("isDetailsOpen");
const chatInputText = defineModel<string>("chatInputText");
const isTerminalOpen = defineModel<boolean>("isTerminalOpen", { default: false });
const isArtifactDrawerOpen = defineModel<boolean>("isArtifactDrawerOpen", { default: false });
const isVCSSidebarOpen = defineModel<boolean>("isVCSSidebarOpen", { default: true });

// Artifacts state management
const activeArtifactPath = ref<string | null>(null);
const modifiedFiles = ref<string[]>([]);

const emit = defineEmits<{
  (e: "send", text: string): void;
  (e: "open-diff", gitRoot: string): void;
  (e: "close-diff"): void;
  (e: "toggle-terminal"): void;
  (e: "toggle-sidebar"): void;
  (e: "open-search"): void;
  (e: "ask-replied", msgId?: string, text?: string): void;
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

  // 2. Collect artifacts from message artifactFiles
  if (props.messages && props.messages.length > 0) {
    for (const msg of props.messages) {
      if (msg.artifactFiles && msg.artifactFiles.length > 0) {
        for (const f of msg.artifactFiles) {
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

function navigateToChat() {
  if (props.sessionId) {
    router.push(buildChatRoute(props.sessionId));
  } else {
    activeView.value = "chat";
  }
}

function navigateToFiles() {
  if (props.sessionId) {
    router.push(buildFilesRoute(props.sessionId, selectedFilePath.value));
  } else {
    activeView.value = "file";
  }
}

function navigateToVcs(gitRoot?: string) {
  if (gitRoot) {
    emit("open-diff", gitRoot);
  }
  if (props.sessionId) {
    router.push(buildVcsRoute(props.sessionId, selectedCommit.value, selectedFilePath.value));
  } else {
    activeView.value = "vcs";
  }
}
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden relative">
    <!-- Top Area: DiffView / FileView / ChatArea -->
    <div class="flex-1 flex flex-col h-full overflow-hidden relative min-h-0">
      <!-- VCS View -->
      <DiffView
        v-if="activeView === 'vcs'"
        :sessionId="sessionId"
        :runDir="runDir"
        :gitRoot="gitRoot"
        :isTerminalOpen="isTerminalOpen"
        v-model:selectedCommit="selectedCommit"
        v-model:selectedFilePath="selectedFilePath"
        v-model:chatInputText="chatInputText"
        v-model:isVCSSidebarOpen="isVCSSidebarOpen"
        @close="navigateToChat"
        @open-file-view="navigateToFiles"
        @toggle-terminal="emit('toggle-terminal')"
      />

      <!-- File View -->
      <FileView
        v-else-if="activeView === 'file'"
        :sessionId="sessionId"
        :runDir="runDir"
        :gitRoot="gitRoot"
        :isTerminalOpen="isTerminalOpen"
        v-model:isFileTreeOpen="isFileTreeOpen"
        v-model:chatInputText="chatInputText"
        v-model:selectedFilePath="selectedFilePath"
        @close="navigateToChat"
        @open-vcs="navigateToVcs()"
        @toggle-terminal="emit('toggle-terminal')"
        @open-search="emit('open-search')"
      />

      <!-- Chat View -->
      <ChatArea
        v-else
        :messages="messages"
        :loading="loading"
        :activeAgent="activeAgent"
        :agents="agents"
        :runDir="runDir"
        :sessionId="sessionId"
        :isTerminalOpen="isTerminalOpen"
        :modifiedFiles="modifiedFiles"
        :activeArtifactPath="activeArtifactPath"
        :isArtifactDrawerOpen="isArtifactDrawerOpen"
        :workingAgentLabel="workingAgentLabel"
        v-model:isDetailsOpen="isDetailsOpen"
        @open-diff="navigateToVcs"
        @open-file-view="navigateToFiles"
        @open-artifact="handleOpenArtifact"
        @select-artifact="(f) => (activeArtifactPath = f)"
        @toggle-terminal="emit('toggle-terminal')"
        @toggle-sidebar="emit('toggle-sidebar')"
        @toggle-artifact-drawer="toggleArtifactDrawer"
        @ask-replied="(msgId, text) => emit('ask-replied', msgId, text)"
      />
    </div>

    <!-- Bottom Area: WorkflowControlPanel / ChatInput (Chat View only) & TerminalPanel -->
    <div class="w-full shrink-0 flex flex-col z-20">
      <template v-if="activeView === 'chat'">
        <WorkflowControlPanel
          v-if="activeAgent?.type === 'workflow'"
          :activeAgent="activeAgent"
          :loading="loading"
          :workingAgentLabel="workingAgentLabel"
          :messages="messages"
          :sessionId="sessionId"
          @ask-replied="(msgId, text) => emit('ask-replied', msgId, text)"
          @open-artifact="(file) => handleOpenArtifact(file)"
        />
        <ChatInput
          v-else
          @send="(text) => emit('send', text)"
          :loading="loading"
          v-model="chatInputText"
        />
      </template>
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
