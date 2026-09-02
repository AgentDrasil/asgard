<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useRouter } from "vue-router";
import { Icon } from "@iconify/vue";
import { useToast } from "../composables/useToast";
import { getSessions, archiveSession } from "../lib/api";
import { formatTimestamp } from "../lib/format";
import type { ChatSession } from "../types";

const router = useRouter();
const toast = useToast();

const activeSessions = ref<ChatSession[]>([]);
const archivedSessions = ref<ChatSession[]>([]);
const isLoading = ref(false);
const isArchiving = ref<Record<string, boolean>>({});
const searchQuery = ref("");
const viewMode = ref<"kanban" | "archived">("kanban");

const THREE_HOURS_MS = 3 * 3600 * 1000;

const fetchAllSessions = async () => {
  isLoading.value = true;
  try {
    const [active, archived] = await Promise.all([getSessions(false), getSessions(true)]);
    activeSessions.value = active;
    archivedSessions.value = archived;
  } catch (err) {
    console.error("Failed to load sessions for dashboard:", err);
    toast.error("Failed to load session list");
  } finally {
    isLoading.value = false;
  }
};

onMounted(() => {
  void fetchAllSessions();
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

// 3 Kanban Columns for active sessions
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
      toast.success("Session archived successfully");
      // Remove from activeSessions and add to archivedSessions
      activeSessions.value = activeSessions.value.filter((s) => s.chatID !== chatID);
      const updatedSession: ChatSession = { ...session, isArchived: true };
      archivedSessions.value = [
        updatedSession,
        ...archivedSessions.value.filter((s) => s.chatID !== chatID),
      ];
    } else {
      toast.error("Failed to archive session, please try again");
    }
  } catch (err) {
    console.error("Archive error:", err);
    toast.error("Unexpected error while archiving");
  } finally {
    isArchiving.value[chatID] = false;
  }
};

const formatRelativeTime = (timeStr?: string): string => {
  if (!timeStr) return "-";
  const date = new Date(timeStr);
  const diff = Date.now() - date.getTime();
  if (isNaN(diff) || diff < 0) return "-";
  if (diff < 60 * 1000) return "just now";
  if (diff < 3600 * 1000) return `${Math.floor(diff / (60 * 1000))} minutes ago`;
  if (diff < 24 * 3600 * 1000) return `${Math.floor(diff / (3600 * 1000))} hours ago`;
  return `${Math.floor(diff / (24 * 3600 * 1000))} days ago`;
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
              Session Board
              <span
                class="text-xs font-normal px-2 py-0.5 rounded-full bg-base-300 text-base-content/70"
              >
                Dashboard
              </span>
            </h1>
            <p class="text-xs text-base-content/60 mt-0.5">
              Real-time monitoring of multi-agent session status and progress
            </p>
          </div>
        </div>

        <!-- Metrics Badges -->
        <div class="flex flex-wrap items-center gap-2 text-xs">
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-primary/10 text-primary border border-primary/20"
          >
            <span class="w-2 h-2 rounded-full bg-primary animate-pulse"></span>
            <span
              >Running: <strong>{{ totalRunningCount }}</strong></span
            >
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-warning/10 text-warning border border-warning/20"
          >
            <span class="w-2 h-2 rounded-full bg-warning"></span>
            <span
              >Waiting: <strong>{{ totalWaitingCount }}</strong></span
            >
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-success/10 text-success border border-success/20"
          >
            <span class="w-2 h-2 rounded-full bg-success"></span>
            <span
              >Recently Completed (&lt; 3h): <strong>{{ totalRecentCompletedCount }}</strong></span
            >
          </div>
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-base-300 text-base-content/70 border border-base-300"
          >
            <Icon icon="lucide:archive" class="w-3.5 h-3.5" />
            <span
              >Archived: <strong>{{ totalArchivedCount }}</strong></span
            >
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
            Kanban View
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
            View Archive
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
              placeholder="Search sessions by title, agent, path..."
              class="input input-sm input-bordered w-full pl-9 pr-3 bg-base-100 focus:outline-primary text-sm"
              data-test="search-input"
            />
          </div>
          <button
            type="button"
            class="btn btn-sm btn-outline btn-square border-base-300 hover:bg-base-200"
            :disabled="isLoading"
            @click="fetchAllSessions"
            title="Refresh data"
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
        <p class="text-sm text-base-content/60">Loading session data...</p>
      </div>

      <!-- Mode 1: Active Kanban View (3 Columns) -->
      <div
        v-else-if="viewMode === 'kanban'"
        class="grid grid-cols-1 md:grid-cols-3 gap-6 items-start"
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
              <h2 class="font-semibold text-sm text-base-content">Running</h2>
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
                  <span class="badge badge-xs badge-primary animate-pulse shrink-0">Running</span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    title="Archive session"
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
                  <Icon icon="lucide:bot" class="w-3.5 h-3.5 text-primary" />
                  {{ session.currentAgent || "default" }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ session.runDir }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  Active: {{ formatRelativeTime(session.updatedAt || session.createdAt) }}
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
              <p class="text-xs">No running tasks</p>
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
              <h2 class="font-semibold text-sm text-base-content">Waiting for User</h2>
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
                  <span class="badge badge-xs badge-warning shrink-0">Awaiting Reply</span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    title="Archive session"
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
                  <Icon icon="lucide:bot" class="w-3.5 h-3.5 text-warning" />
                  {{ session.currentAgent || "default" }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ session.runDir }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  Asked: {{ formatRelativeTime(session.updatedAt || session.createdAt) }}
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
              <p class="text-xs">No questions awaiting reply</p>
            </div>
          </div>
        </div>

        <!-- Column 3: Recently Completed / Idle (< 3h) -->
        <div
          class="flex flex-col rounded-xl bg-base-200/50 border border-base-300 p-4 min-h-[500px]"
          data-test="column-completed"
        >
          <div class="flex items-center justify-between pb-3 mb-3 border-b border-base-300">
            <div class="flex items-center gap-2">
              <span class="w-2.5 h-2.5 rounded-full bg-success"></span>
              <h2 class="font-semibold text-sm text-base-content">
                Recently Completed / Idle (&lt; 3h)
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
              data-test="session-card-completed"
            >
              <div class="flex items-start justify-between gap-2">
                <h3
                  class="font-medium text-sm text-base-content line-clamp-2 flex-1 group-hover:text-success transition-colors"
                >
                  {{ session.title || session.chatID }}
                </h3>
                <div class="flex items-center gap-1">
                  <span class="badge badge-xs badge-success shrink-0">Done</span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
                    title="Archive session"
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
                  <Icon icon="lucide:bot" class="w-3.5 h-3.5 text-success" />
                  {{ session.currentAgent || "default" }}
                </span>
                <span
                  v-if="session.runDir"
                  class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                  :title="session.runDir"
                >
                  <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                  {{ session.runDir }}
                </span>
              </div>

              <div
                class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
              >
                <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                  Completed: {{ formatRelativeTime(session.updatedAt || session.createdAt) }}
                </span>
                <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
              </div>
            </div>

            <!-- Empty State -->
            <div
              v-if="recentCompletedSessions.length === 0"
              class="flex-1 flex flex-col items-center justify-center p-6 text-center text-base-content/40 border border-dashed border-base-300 rounded-lg"
              data-test="empty-completed"
            >
              <Icon icon="lucide:clock" class="w-8 h-8 mb-2 opacity-50" />
              <p class="text-xs">No sessions completed in the last 3 hours</p>
            </div>
          </div>
        </div>
      </div>

      <!-- Mode 2: View Archived View -->
      <div v-else class="flex flex-col gap-4" data-test="archived-view">
        <div class="flex items-center justify-between pb-2 border-b border-base-300">
          <div class="flex items-center gap-2">
            <Icon icon="lucide:archive" class="w-5 h-5 text-base-content/70" />
            <h2 class="font-bold text-base text-base-content">Archived Sessions</h2>
            <span class="badge badge-sm badge-neutral font-mono">{{
              filteredArchivedSessions.length
            }}</span>
          </div>
          <p class="text-xs text-base-content/50">
            Click any archived card to view the full conversation history and artifacts
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
              <span class="badge badge-xs badge-neutral shrink-0">Archived</span>
            </div>

            <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
              <span
                class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-100 font-mono"
              >
                <Icon icon="lucide:bot" class="w-3.5 h-3.5 text-base-content/70" />
                {{ session.currentAgent || "default" }}
              </span>
              <span
                v-if="session.runDir"
                class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
                :title="session.runDir"
              >
                <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
                {{ session.runDir }}
              </span>
            </div>

            <div
              class="mt-3 pt-2 border-t border-base-300/50 flex items-center justify-between text-[11px] text-base-content/50"
            >
              <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
                Archived: {{ formatRelativeTime(session.updatedAt || session.createdAt) }}
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
          <h3 class="font-medium text-sm text-base-content/70">No archived sessions</h3>
          <p class="text-xs text-base-content/50 mt-1">
            Click the archive button on a card in the Kanban view to move sessions into this list
          </p>
        </div>
      </div>
    </main>
  </div>
</template>
