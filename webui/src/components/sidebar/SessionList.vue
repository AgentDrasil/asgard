<script setup lang="ts">
import { ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import type { ChatSession, AgentInfo } from "../../types";
import { getAgentIcon } from "../../utils/agentUtils";
import SessionItem from "../SessionItem.vue";

const { t } = useI18n();

const props = defineProps<{
  sessions: ChatSession[];
  agents?: AgentInfo[];
  activeSessionId: string | null;
  viewMode: "list" | "agent";
}>();

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "delete-session", id: string): void;
  (e: "archive-session", id: string): void;
  (e: "new-chat", agentId?: string, runDir?: string): void;
}>();

// Collapsed state tracking
const collapsedAgents = ref<Record<string, boolean>>({});
const collapsedWorkspaces = ref<Record<string, boolean>>({});

const toggleAgentCollapse = (agentName: string) => {
  collapsedAgents.value[agentName] = !collapsedAgents.value[agentName];
};

const toggleWorkspaceCollapse = (key: string) => {
  collapsedWorkspaces.value[key] = !collapsedWorkspaces.value[key];
};

// Helper for display path of runDir / workspace
const formatWorkspaceName = (runDir?: string): string => {
  if (!runDir) return t("sidebar.defaultWorkspace");
  const trimmed = runDir.replace(/[/\\]+$/, "");
  const parts = trimmed.split(/[/\\]/);
  return parts[parts.length - 1] || runDir;
};

// Nested 3-level structure: Agent -> Workspace -> Sessions
interface WorkspaceGroup {
  runDir: string;
  displayName: string;
  sessions: ChatSession[];
}

interface AgentNestedGroup {
  agentName: string;
  workspaces: WorkspaceGroup[];
  totalSessions: number;
}

const nestedGroupedSessions = computed<AgentNestedGroup[]>(() => {
  const agentMap: Record<string, Record<string, ChatSession[]>> = {};

  for (const session of props.sessions) {
    const agentKey = session.currentAgent || t("sidebar.unknownAgent");
    const dirKey = session.runDir || t("sidebar.defaultWorkspace");

    if (!agentMap[agentKey]) {
      agentMap[agentKey] = {};
    }
    if (!agentMap[agentKey][dirKey]) {
      agentMap[agentKey][dirKey] = [];
    }
    agentMap[agentKey][dirKey].push(session);
  }

  return Object.entries(agentMap).map(([agentName, workspacesObj]) => {
    let totalSessions = 0;
    const workspaces: WorkspaceGroup[] = Object.entries(workspacesObj).map(([runDir, sessions]) => {
      totalSessions += sessions.length;
      return {
        runDir,
        displayName: formatWorkspaceName(runDir),
        sessions,
      };
    });

    return {
      agentName,
      workspaces,
      totalSessions,
    };
  });
});
</script>

<template>
  <div v-if="sessions.length === 0" class="text-xs text-base-content/50 text-center py-6">
    {{ t("sidebar.noActiveSessions") }}
  </div>

  <!-- 1. List Mode -->
  <template v-else-if="viewMode === 'list'">
    <div class="w-full space-y-1 rounded-lg overflow-hidden">
      <SessionItem
        v-for="session in sessions"
        :key="session.chatID"
        :session="session"
        :is-active="activeSessionId === session.chatID"
        @select-session="emit('select-session', $event)"
        @delete-session="emit('delete-session', $event)"
        @archive-session="emit('archive-session', $event)"
      />
    </div>
  </template>

  <!-- 2. Group by Agent & Workspace Mode (3-Level Collapsible) -->
  <template v-else-if="viewMode === 'agent'">
    <div
      v-for="agentGroup in nestedGroupedSessions"
      :key="agentGroup.agentName"
      class="w-full space-y-1 mb-2"
    >
      <!-- Level 1: Agent Header -->
      <div
        @click="toggleAgentCollapse(agentGroup.agentName)"
        class="px-2 py-1 flex items-center justify-between text-sm font-semibold text-primary/90 select-none cursor-pointer hover:bg-base-200/50 rounded-md transition-colors"
      >
        <div class="flex items-center gap-1.5 min-w-0">
          <Icon
            icon="mynaui:chevron-down"
            :class="[
              'h-4 w-4 fill-current shrink-0 transition-transform duration-200',
              collapsedAgents[agentGroup.agentName] ? '-rotate-90' : '',
            ]"
          />
          <Icon :icon="getAgentIcon(agentGroup.agentName, agents)" class="h-4.5 w-4.5 shrink-0" />
          <span class="truncate">{{ agentGroup.agentName }}</span>
        </div>
        <div class="flex items-center gap-1.5 shrink-0">
          <button
            @click.stop="emit('new-chat', agentGroup.agentName)"
            class="btn btn-ghost btn-xs p-1 h-6 min-h-0 w-6 rounded text-base-content/70 hover:text-primary hover:bg-base-300 flex items-center justify-center"
            :title="t('sidebar.newChatWithAgent')"
          >
            <Icon icon="mynaui:plus" class="h-4 w-4 fill-current stroke-[2.5]" />
          </button>
          <span class="text-xs text-base-content/40 font-normal"
            >({{ agentGroup.totalSessions }})</span
          >
        </div>
      </div>

      <!-- Level 2: Workspaces List -->
      <template v-if="!collapsedAgents[agentGroup.agentName]">
        <div v-for="wsGroup in agentGroup.workspaces" :key="wsGroup.runDir" class="pl-2 space-y-1">
          <!-- Workspace Header -->
          <div
            @click="toggleWorkspaceCollapse(`${agentGroup.agentName}:${wsGroup.runDir}`)"
            class="px-2 py-1 flex items-center justify-between text-xs font-medium text-base-content/80 select-none cursor-pointer hover:bg-base-200/40 rounded-md transition-colors"
            :title="wsGroup.runDir"
          >
            <div class="flex items-center gap-1.5 min-w-0">
              <Icon
                icon="mynaui:chevron-down"
                :class="[
                  'h-3.5 w-3.5 fill-current shrink-0 transition-transform duration-200 opacity-70',
                  collapsedWorkspaces[`${agentGroup.agentName}:${wsGroup.runDir}`]
                    ? '-rotate-90'
                    : '',
                ]"
              />
              <Icon icon="mynaui:folder" class="h-4 w-4 shrink-0 opacity-70" />
              <span class="truncate">{{ wsGroup.displayName }}</span>
            </div>
            <div class="flex items-center gap-1.5 shrink-0">
              <button
                @click.stop="emit('new-chat', agentGroup.agentName, wsGroup.runDir)"
                class="btn btn-ghost btn-xs p-1 h-6 min-h-0 w-6 rounded text-base-content/70 hover:text-primary hover:bg-base-300 flex items-center justify-center"
                :title="t('sidebar.newChatWithAgentAndWorkspace')"
              >
                <Icon icon="mynaui:plus" class="h-4 w-4 fill-current stroke-[2.5]" />
              </button>
              <span class="text-xs text-base-content/40">({{ wsGroup.sessions.length }})</span>
            </div>
          </div>

          <!-- Level 3: Sessions List under Workspace -->
          <template v-if="!collapsedWorkspaces[`${agentGroup.agentName}:${wsGroup.runDir}`]">
            <div class="pl-2 space-y-0.5">
              <SessionItem
                v-for="session in wsGroup.sessions"
                :key="session.chatID"
                :session="session"
                :is-active="activeSessionId === session.chatID"
                @select-session="emit('select-session', $event)"
                @delete-session="emit('delete-session', $event)"
                @archive-session="emit('archive-session', $event)"
              />
            </div>
          </template>
        </div>
      </template>
    </div>
  </template>
</template>
