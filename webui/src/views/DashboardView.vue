<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { useToast } from "../composables/useToast";
import { getSessions, archiveSession, getAgents } from "../lib/api";
import { formatTimestamp } from "../lib/format";
import { formatRelativeTime } from "../i18n/timeUtils";
import { getAgentIcon, formatPath } from "../utils/agentUtils";
import type { ChatSession, AgentInfo } from "../types";

const router = useRouter();
const { t } = useI18n();
const toast = useToast();

const activeSessions = ref<ChatSession[]>([]);
const archivedSessions = ref<ChatSession[]>([]);
const agents = ref<AgentInfo[]>([]);
const isLoading = ref(false);
const isPolling = ref(false);
const isArchiving = ref<Record<string, boolean>>({});
const searchQuery = ref("");
const viewMode = ref<"kanban" | "archived">("kanban");

const THREE_HOURS_MS = 3 * 3600 * 1000;

let pollInterval: ReturnType<typeof setInterval> | null = null;

const fetchAllSessions = async () => {
  isLoading.value = true;
  try {
    const [active, archived, agentList] = await Promise.all([
      getSessions(false),
      getSessions(true),
      getAgents(),
    ]);
    activeSessions.value = active;
    archivedSessions.value = archived;
    agents.value = agentList;
  } catch (err) {
    console.error("Failed to load sessions for dashboard:", err);
    toast.error(t("dashboard.loadFailed"));
  } finally {
    isLoading.value = false;
  }
};

const pollSessions = async () => {
  if (isPolling.value) return;
  isPolling.value = true;
  try {
    const active = await getSessions(false);
    // getSessions swallows errors and returns []; skip the update so a
    // transient failure cannot blank the board (archive flow already
    // updates activeSessions optimistically, manual refresh does a full fetch).
    if (active.length > 0) {
      activeSessions.value = active;
    }
  } catch (err) {
    console.error("Failed to poll active sessions for dashboard:", err);
  } finally {
    isPolling.value = false;
  }
};

onMounted(() => {
  void fetchAllSessions();
  pollInterval = setInterval(() => {
    void pollSessions();
  }, 5000);
});

onUnmounted(() => {
  if (pollInterval) {
    clearInterval(pollInterval);
    pollInterval = null;
  }
});

const filterSession = (session: ChatSession, query: string): boolean => {
  if (!query) return true;
  const q = query.toLowerCase().trim();
  const title = (session.title || "").toLowerCase();
  const agent = (session.currentAgent || "").toLowerCase();
  const dir = (session.runDir || "").toLowerCase();
  const id = (session.chatID || "").toLowerCase();
  return title.includes(q) || agent.includes(q) || dir.includes(q) || id.includes(q);
};

// 4 Kanban Columns for active sessions
const runningSessions = computed(() => {
  return activeSessions.value.filter(
    (s) =>
      !s.isArchived && s.isRunning && !s.isWaitingForUser && filterSession(s, searchQuery.value),
  );
});

const waitingSessions = computed(() => {
  return activeSessions.value.filter(
    (s) => !s.isArchived && s.isWaitingForUser && filterSession(s, searchQuery.value),
  );
});

const recentCompletedSessions = computed(() => {
  const now = Date.now();
  return activeSessions.value.filter((s) => {
    if (s.isArchived || s.isRunning || s.isWaitingForUser) return false;
    const time = new Date(s.updatedAt || s.createdAt || 0).getTime();
    return now - time < THREE_HOURS_MS && filterSession(s, searchQuery.value);
  });
});

const completedSessions = computed(() => {
  const now = Date.now();
  return activeSessions.value.filter((s) => {
    if (s.isArchived || s.isRunning || s.isWaitingForUser) return false;
    const time = new Date(s.updatedAt || s.createdAt || 0).getTime();
    return now - time >= THREE_HOURS_MS && filterSession(s, searchQuery.value);
  });
});

const filteredArchivedSessions = computed(() => {
  return archivedSessions.value.filter((s) => s.isArchived && filterSession(s, searchQuery.value));
});

// Total count metrics
const totalRunningCount = computed(() => {
  return activeSessions.value.filter((s) => !s.isArchived && s.isRunning && !s.isWaitingForUser)
    .length;
});

const totalWaitingCount = computed(() => {
  return activeSessions.value.filter((s) => !s.isArchived && s.isWaitingForUser).length;
});

const totalRecentCompletedCount = computed(() => {
  const now = Date.now();
  return activeSessions.value.filter((s) => {
    if (s.isArchived || s.isRunning || s.isWaitingForUser) return false;
    const time = new Date(s.updatedAt || s.createdAt || 0).getTime();
    return now - time < THREE_HOURS_MS;
  }).length;
});

const totalCompletedCount = computed(() => {
  const now = Date.now();
  return activeSessions.value.filter((s) => {
    if (s.isArchived || s.isRunning || s.isWaitingForUser) return false;
    const time = new Date(s.updatedAt || s.createdAt || 0).getTime();
    return now - time >= THREE_HOURS_MS;
  }).length;
});

const totalArchivedCount = computed(() => {
  return archivedSessions.value.length;
});

const handleNavigate = (chatID: string) => {
  if (!chatID) return;
  router.push(`/chat/${encodeURIComponent(chatID)}`);
};

const handleArchive = async (session: ChatSession, event?: Event) => {
  if (event) {
    event.stopPropagation();
  }
  const chatID = session.chatID;
  if (!chatID || isArchiving.value[chatID]) return;

  isArchiving.value[chatID] = true;
  try {
    const success = await archiveSession(chatID);
    if (success) {
      toast.success(t("dashboard.archiveSuccess"));
      // Remove from activeSessions and add to archivedSessions
      activeSessions.value = activeSessions.value.filter((s) => s.chatID !== chatID);
      const updatedSession: ChatSession = { ...session, isArchived: true };
      archivedSessions.value = [
        updatedSession,
        ...archivedSessions.value.filter((s) => s.chatID !== chatID),
      ];
    } else {
      toast.error(t("dashboard.archiveFailed"));
    }
  } catch (err) {
    console.error("Archive error:", err);
    toast.error(t("dashboard.archiveUnexpectedError"));
  } finally {
    isArchiving.value[chatID] = false;
  }
};
</script>

<template>
  <div class="flex-1 flex flex-col h-full bg-base-100 overflow-y-auto">
    <!-- Top Header & Controls -->
    <header class="border-b border-base-300 bg-base-200/50 px-6 py-4">
      <div class="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4">
        <!-- Title & Stats -->
        <div class="flex items-center gap-3">
          <div class="p-2.5 rounded-lg bg-primary/10 text-primary">
            <Icon icon="lucide:layout-dashboard" class="w-6 h-6" />
          </div>
          <div>
            <h1 class="text-xl font-bold tracking-tight text-base-content flex items-center gap-2">
              {{ t("dashboard.title") }}
              <span
                class="text-xs font-normal px-2 py-0.5 rounded-full bg-base-300 text-base-content/70"
              >
                {{ t("dashboard.badge") }}
              </span>
            </h1>
            <p class="text-xs text-base-content/60 mt-0.5">
              {{ t("dashboard.subtitle") }}
            </p>
          </div>
        </div>

        <!-- Metrics Badges -->
        <div class="flex flex-wrap items-center gap-2 text-xs">
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-primary/10 text-primary border border-primary/20"
          >
            <span class="w-2 h-2 rounded-full bg-primary animate-pulse"></span>
            <span>
              <strong>{{ t("dashboard.metrics.running", { count: totalRunningCount }) }}</strong>
            </span>
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-warning/10 text-warning border border-warning/20"
          >
            <span class="w-2 h-2 rounded-full bg-warning"></span>
            <span>
              <strong>{{ t("dashboard.metrics.waiting", { count: totalWaitingCount }) }}</strong>
            </span>
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-success/10 text-success border border-success/20"
          >
            <span class="w-2 h-2 rounded-full bg-success"></span>
            <span>
              <strong>{{
                t("dashboard.metrics.recentCompleted", { count: totalRecentCompletedCount })
              }}</strong>
            </span>
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-base-200 text-base-content/80 border border-base-300"
          >
            <span class="w-2 h-2 rounded-full bg-base-content/40"></span>
            <span>
              <strong>{{
                t("dashboard.metrics.completed", { count: totalCompletedCount })
              }}</strong>
            </span>
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-base-300 text-base-content/70 border border-base-300"
          >
            <Icon icon="lucide:archive" class="w-3.5 h-3.5" />
            <span>
              <strong>{{ t("dashboard.metrics.archived", { count: totalArchivedCount }) }}</strong>
            </span>
          </div>
        </div>
      </div>

      <!-- Action Bar: Tabs, Search, Refresh -->
      <div
        class="mt-4 flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3"
      >
        <!-- View Mode Switcher -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg border border-base-300 self-start">
          <button
            type="button"
            class="join-item btn btn-sm border-none gap-2 font-medium"
            :class="
              viewMode === 'kanban' ? 'btn-primary shadow-sm' : 'btn-ghost text-base-content/70'
            "
            @click="viewMode = 'kanban'"
            data-test="tab-kanban"
          >
            <Icon icon="lucide:kanban" class="w-4 h-4" />
            {{ t("dashboard.tabs.kanban") }}
          </button>
          <button
            type="button"
            class="join-item btn btn-sm border-none gap-2 font-medium"
            :class="
              viewMode === 'archived' ? 'btn-primary shadow-sm' : 'btn-ghost text-base-content/70'
            "
            @click="viewMode = 'archived'"
            data-test="tab-archived"
          >
            <Icon icon="lucide:archive" class="w-4 h-4" />
            {{ t("dashboard.tabs.archived") }}
            <span v-if="totalArchivedCount > 0" class="badge badge-xs badge-neutral">
              {{ totalArchivedCount }}
            </span>
          </button>
        </div>

        <!-- Search and Refresh -->
        <div class="flex items-center gap-2 flex-1 sm:max-w-md">
          <div class="relative flex-1">
            <Icon
              icon="lucide:search"
              class="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-base-content/40"
            />
            <input
              v-model="searchQuery"
              type="text"
              :placeholder="t('dashboard.searchPlaceholder')"
              class="input input-sm input-bordered w-full pl-9 pr-3 bg-base-100 focus:outline-primary text-sm"
              data-test="search-input"
            />
          </div>
          <button
            type="button"
            class="btn btn-sm btn-outline btn-square border-base-300 hover:bg-base-200"
            :disabled="isLoading"
            @click="fetchAllSessions"
            :title="t('dashboard.refreshTooltip')"
            data-test="btn-refresh"
          >
            <Icon icon="lucide:refresh-cw" class="w-4 h-4" :class="{ 'animate-spin': isLoading }" />
          </button>
        </div>
      </div>
    </header>

    <!-- Main Content Area -->
    <main class="flex-1 p-6">
      <!-- Loading State -->
      <div
        v-if="isLoading && activeSessions.length === 0 && archivedSessions.length === 0"
        class="flex flex-col items-center justify-center h-64 gap-3"
      >
        <span class="loading loading-spinner loading-lg text-primary"></span>
        <p class="text-sm text-base-content/60">{{ t("dashboard.loading") }}</p>
      </div>

      <!-- Mode 1: Active Kanban View (4 Columns) -->
      <div
        v-else-if="viewMode === 'kanban'"
        class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-6 items-start"
        data-test="kanban-view"
      >
        <!-- Column 1: Running -->
        <div
          class="flex flex-col rounded-xl bg-base-200/50 border border-base-300 p-4 min-h-[500px]"
          data-test="column-running"
        >
          <div class="flex items-center justify-between pb-3 mb-3 border-b border-base-300">
            <div class="flex items-center gap-2">
              <span class="w-2.5 h-2.5 rounded-full bg-primary animate-pulse"></span>
              <h2 class="font-semibold text-sm text-base-content">
                {{ t("dashboard.columns.running") }}
              </h2>
            </div>
            <span class="badge badge-sm badge-primary font-mono">{{ runningSessions.length }}</span>
          </div>

          <!-- Column Cards List -->
          <div class="flex flex-col gap-3 flex-1">
            <div
              v-for="session in runningSessions"
              :key="session.chatID"
              class="card bg-base-100 hover:border-primary/50 border border-base-300 shadow-sm hover:shadow-md transition-all cursor-pointer p-4 group"
              @click="handleNavigate(session.chatID)"
              data-test="session-card-running"
            >
              <div class="flex items-start justify-between gap-2">
                <h3
                  class="font-medium text-sm text-base-content line-clamp-2 flex-1 group-hover:text-primary transition-colors"
                >
                  {{ session.title || session.chatID }}
                </h3>
                <div class="flex items-center gap-1">
                  <span class="badge badge-xs badge-primary animate-pulse shrink-0">
                    {{ t("dashboard.cards.runningBadge") }}
                  </span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    :title="t('dashboard.cards.archiveTooltip')"
                    :disabled="isArchiving[session.chatID]"
                    @click.stop="handleArchive(session, $event)"
                    data-test="btn-archive"
                  >
                    <Icon
                      v-if="isArchiving[session.chatID]"
                      icon="lucide:loader-2"
                      class="w-3.5 h-3.5 animate-spin"
                    />
                    <Icon v-else icon="lucide:archive" class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>

              <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
                <span
                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-200 font-mono"
                >
                  <Icon
                    :icon="getAgentIcon(session.currentAgent, agents)"
                    class="w-3.5 h-3.5 text-primary"
                  />
                  {{ session.currentAgent || t("dashboard.cards.defaultAgent") }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ formatPath(session.runDir) }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  {{
                    t("dashboard.cards.activeTime", {
                      time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                    })
                  }}
                </span>
                <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
              </div>
            </div>

            <!-- Empty State -->
            <div
              v-if="runningSessions.length === 0"
              class="flex-1 flex flex-col items-center justify-center p-6 text-center text-base-content/40 border border-dashed border-base-300 rounded-lg"
              data-test="empty-running"
            >
              <Icon icon="lucide:check-circle-2" class="w-8 h-8 mb-2 opacity-50" />
              <p class="text-xs">{{ t("dashboard.empty.running") }}</p>
            </div>
          </div>
        </div>

        <!-- Column 2: Waiting for User -->
        <div
          class="flex flex-col rounded-xl bg-base-200/50 border border-base-300 p-4 min-h-[500px]"
          data-test="column-waiting"
        >
          <div class="flex items-center justify-between pb-3 mb-3 border-b border-base-300">
            <div class="flex items-center gap-2">
              <span class="w-2.5 h-2.5 rounded-full bg-warning"></span>
              <h2 class="font-semibold text-sm text-base-content">
                {{ t("dashboard.columns.waiting") }}
              </h2>
            </div>
            <span class="badge badge-sm badge-warning font-mono">{{ waitingSessions.length }}</span>
          </div>

          <!-- Column Cards List -->
          <div class="flex flex-col gap-3 flex-1">
            <div
              v-for="session in waitingSessions"
              :key="session.chatID"
              class="card bg-base-100 hover:border-warning/50 border border-base-300 shadow-sm hover:shadow-md transition-all cursor-pointer p-4 group"
              @click="handleNavigate(session.chatID)"
              data-test="session-card-waiting"
            >
              <div class="flex items-start justify-between gap-2">
                <h3
                  class="font-medium text-sm text-base-content line-clamp-2 flex-1 group-hover:text-warning transition-colors"
                >
                  {{ session.title || session.chatID }}
                </h3>
                <div class="flex items-center gap-1">
                  <span class="badge badge-xs badge-warning shrink-0">
                    {{ t("dashboard.cards.waitingBadge") }}
                  </span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    :title="t('dashboard.cards.archiveTooltip')"
                    :disabled="isArchiving[session.chatID]"
                    @click.stop="handleArchive(session, $event)"
                    data-test="btn-archive"
                  >
                    <Icon
                      v-if="isArchiving[session.chatID]"
                      icon="lucide:loader-2"
                      class="w-3.5 h-3.5 animate-spin"
                    />
                    <Icon v-else icon="lucide:archive" class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>

              <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
                <span
                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-200 font-mono"
                >
                  <Icon
                    :icon="getAgentIcon(session.currentAgent, agents)"
                    class="w-3.5 h-3.5 text-warning"
                  />
                  {{ session.currentAgent || t("dashboard.cards.defaultAgent") }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ formatPath(session.runDir) }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  {{
                    t("dashboard.cards.askedTime", {
                      time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                    })
                  }}
                </span>
                <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
              </div>
            </div>

            <!-- Empty State -->
            <div
              v-if="waitingSessions.length === 0"
              class="flex-1 flex flex-col items-center justify-center p-6 text-center text-base-content/40 border border-dashed border-base-300 rounded-lg"
              data-test="empty-waiting"
            >
              <Icon icon="lucide:message-square-check" class="w-8 h-8 mb-2 opacity-50" />
              <p class="text-xs">{{ t("dashboard.empty.waiting") }}</p>
            </div>
          </div>
        </div>

        <!-- Column 3: Recently Completed (< 3h) -->
        <div
          class="flex flex-col rounded-xl bg-base-200/50 border border-base-300 p-4 min-h-[500px]"
          data-test="column-recent-completed"
        >
          <div class="flex items-center justify-between pb-3 mb-3 border-b border-base-300">
            <div class="flex items-center gap-2">
              <span class="w-2.5 h-2.5 rounded-full bg-success"></span>
              <h2 class="font-semibold text-sm text-base-content">
                {{ t("dashboard.columns.recentCompleted") }}
              </h2>
            </div>
            <span class="badge badge-sm badge-success font-mono">{{
              recentCompletedSessions.length
            }}</span>
          </div>

          <!-- Column Cards List -->
          <div class="flex flex-col gap-3 flex-1">
            <div
              v-for="session in recentCompletedSessions"
              :key="session.chatID"
              class="card bg-base-100 hover:border-success/50 border border-base-300 shadow-sm hover:shadow-md transition-all cursor-pointer p-4 group"
              @click="handleNavigate(session.chatID)"
              data-test="session-card-recent-completed"
            >
              <div class="flex items-start justify-between gap-2">
                <h3
                  class="font-medium text-sm text-base-content line-clamp-2 flex-1 group-hover:text-success transition-colors"
                >
                  {{ session.title || session.chatID }}
                </h3>
                <div class="flex items-center gap-1">
                  <span class="badge badge-xs badge-success shrink-0">
                    {{ t("dashboard.cards.recentCompletedBadge") }}
                  </span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    :title="t('dashboard.cards.archiveTooltip')"
                    :disabled="isArchiving[session.chatID]"
                    @click.stop="handleArchive(session, $event)"
                    data-test="btn-archive"
                  >
                    <Icon
                      v-if="isArchiving[session.chatID]"
                      icon="lucide:loader-2"
                      class="w-3.5 h-3.5 animate-spin"
                    />
                    <Icon v-else icon="lucide:archive" class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>

              <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
                <span
                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-200 font-mono"
                >
                  <Icon
                    :icon="getAgentIcon(session.currentAgent, agents)"
                    class="w-3.5 h-3.5 text-success"
                  />
                  {{ session.currentAgent || t("dashboard.cards.defaultAgent") }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ formatPath(session.runDir) }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  {{
                    t("dashboard.cards.completedTime", {
                      time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                    })
                  }}
                </span>
                <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
              </div>
            </div>

            <!-- Empty State -->
            <div
              v-if="recentCompletedSessions.length === 0"
              class="flex-1 flex flex-col items-center justify-center p-6 text-center text-base-content/40 border border-dashed border-base-300 rounded-lg"
              data-test="empty-recent-completed"
            >
              <Icon icon="lucide:clock" class="w-8 h-8 mb-2 opacity-50" />
              <p class="text-xs">{{ t("dashboard.empty.recentCompleted") }}</p>
            </div>
          </div>
        </div>

        <!-- Column 4: Completed -->
        <div
          class="flex flex-col rounded-xl bg-base-200/50 border border-base-300 p-4 min-h-[500px]"
          data-test="column-completed"
        >
          <div class="flex items-center justify-between pb-3 mb-3 border-b border-base-300">
            <div class="flex items-center gap-2">
              <span class="w-2.5 h-2.5 rounded-full bg-base-content/40"></span>
              <h2 class="font-semibold text-sm text-base-content">
                {{ t("dashboard.columns.completed") }}
              </h2>
            </div>
            <span class="badge badge-sm badge-neutral font-mono">{{
              completedSessions.length
            }}</span>
          </div>

          <!-- Column Cards List -->
          <div class="flex flex-col gap-3 flex-1">
            <div
              v-for="session in completedSessions"
              :key="session.chatID"
              class="card bg-base-100 hover:border-base-content/30 border border-base-300 shadow-sm hover:shadow-md transition-all cursor-pointer p-4 group"
              @click="handleNavigate(session.chatID)"
              data-test="session-card-completed"
            >
              <div class="flex items-start justify-between gap-2">
                <h3
                  class="font-medium text-sm text-base-content line-clamp-2 flex-1 group-hover:text-primary transition-colors"
                >
                  {{ session.title || session.chatID }}
                </h3>
                <div class="flex items-center gap-1">
                  <span class="badge badge-xs badge-neutral shrink-0">
                    {{ t("dashboard.cards.completedBadge") }}
                  </span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    :title="t('dashboard.cards.archiveTooltip')"
                    :disabled="isArchiving[session.chatID]"
                    @click.stop="handleArchive(session, $event)"
                    data-test="btn-archive"
                  >
                    <Icon
                      v-if="isArchiving[session.chatID]"
                      icon="lucide:loader-2"
                      class="w-3.5 h-3.5 animate-spin"
                    />
                    <Icon v-else icon="lucide:archive" class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>

              <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
                <span
                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-200 font-mono"
                >
                  <Icon
                    :icon="getAgentIcon(session.currentAgent, agents)"
                    class="w-3.5 h-3.5 text-base-content/70"
                  />
                  {{ session.currentAgent || t("dashboard.cards.defaultAgent") }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ formatPath(session.runDir) }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  {{
                    t("dashboard.cards.completedTime", {
                      time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                    })
                  }}
                </span>
                <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
              </div>
            </div>

            <!-- Empty State -->
            <div
              v-if="completedSessions.length === 0"
              class="flex-1 flex flex-col items-center justify-center p-6 text-center text-base-content/40 border border-dashed border-base-300 rounded-lg"
              data-test="empty-completed"
            >
              <Icon icon="lucide:check-circle-2" class="w-8 h-8 mb-2 opacity-50" />
              <p class="text-xs">{{ t("dashboard.empty.completed") }}</p>
            </div>
          </div>
        </div>
      </div>

      <!-- Mode 2: View Archived View -->
      <div v-else class="flex flex-col gap-4" data-test="archived-view">
        <div class="flex items-center justify-between pb-2 border-b border-base-300">
          <div class="flex items-center gap-2">
            <Icon icon="lucide:archive" class="w-5 h-5 text-base-content/70" />
            <h2 class="font-bold text-base text-base-content">
              {{ t("dashboard.archivedSection.title") }}
            </h2>
            <span class="badge badge-sm badge-neutral font-mono">{{
              filteredArchivedSessions.length
            }}</span>
          </div>
          <p class="text-xs text-base-content/50">
            {{ t("dashboard.archivedSection.desc") }}
          </p>
        </div>

        <!-- Archived Grid -->
        <div
          v-if="filteredArchivedSessions.length > 0"
          class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4"
        >
          <div
            v-for="session in filteredArchivedSessions"
            :key="session.chatID"
            class="card bg-base-200/60 hover:bg-base-200 border border-base-300 shadow-sm hover:shadow-md transition-all cursor-pointer p-4 group"
            @click="handleNavigate(session.chatID)"
            data-test="session-card-archived"
          >
            <div class="flex items-start justify-between gap-2">
              <h3
                class="font-medium text-sm text-base-content line-clamp-2 flex-1 group-hover:text-primary transition-colors"
              >
                {{ session.title || session.chatID }}
              </h3>
              <span class="badge badge-xs badge-neutral shrink-0">
                {{ t("dashboard.cards.archivedBadge") }}
              </span>
            </div>

            <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
              <span
                class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-100 font-mono"
              >
                <Icon
                  :icon="getAgentIcon(session.currentAgent, agents)"
                  class="w-3.5 h-3.5 text-base-content/70"
                />
                {{ session.currentAgent || t("dashboard.cards.defaultAgent") }}
              </span>
              <span
                v-if="session.runDir"
                class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                :title="session.runDir"
              >
                <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                {{ formatPath(session.runDir) }}
              </span>
            </div>

            <div
              class="mt-3 pt-2 border-t border-base-300/50 flex items-center justify-between text-[11px] text-base-content/50"
            >
              <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                {{
                  t("dashboard.cards.archivedTime", {
                    time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                  })
                }}
              </span>
              <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
            </div>
          </div>
        </div>

        <!-- Empty State for Archived -->
        <div
          v-else
          class="flex flex-col items-center justify-center p-12 text-center text-base-content/40 border border-dashed border-base-300 rounded-xl bg-base-200/20"
          data-test="empty-archived"
        >
          <Icon icon="lucide:archive-x" class="w-12 h-12 mb-3 opacity-40" />
          <h3 class="font-medium text-sm text-base-content/70">
            {{ t("dashboard.empty.archivedTitle") }}
          </h3>
          <p class="text-xs text-base-content/50 mt-1">
            {{ t("dashboard.empty.archivedDesc") }}
          </p>
        </div>
      </div>
    </main>
  </div>
</template>
