<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import type { ChatSession, AgentInfo } from "../types";
import { Icon } from "@iconify/vue";
import { apiFetch } from "../lib/api";
import SessionItem from "./SessionItem.vue";

const props = withDefaults(
  defineProps<{
    sessions: ChatSession[];
    agents?: AgentInfo[];
    activeSessionId: string | null;
    isOpen?: boolean;
  }>(),
  {
    isOpen: true,
    agents: () => [],
  },
);

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "new-chat"): void;
  (e: "delete-session", id: string): void;
  (e: "toggle-sidebar"): void;
  (e: "toggle-terminal"): void;
}>();

const currentTheme = ref("dark");
const isReloading = ref(false);
const viewMode = ref<"list" | "agent">("list");
const collapsedGroups = ref<Record<string, boolean>>({});

const toggleGroupCollapse = (agentName: string) => {
  collapsedGroups.value[agentName] = !collapsedGroups.value[agentName];
};

const toggleViewMode = (mode: "list" | "agent") => {
  viewMode.value = mode;
};

const getAgentIcon = (agentName: string): string => {
  const matched = props.agents?.find((a) => a.name === agentName || a.id === agentName);
  return matched?.icon || "fluent-color:bot-24";
};

// Computed property to group sessions by currentAgent
const groupedSessions = computed(() => {
  const groups: Record<string, ChatSession[]> = {};
  for (const session of props.sessions) {
    const agentKey = session.currentAgent || "Unknown Agent";
    if (!groups[agentKey]) {
      groups[agentKey] = [];
    }
    groups[agentKey].push(session);
  }
  return groups;
});

const reloadApp = async () => {
  if (isReloading.value) return;
  isReloading.value = true;
  try {
    await apiFetch("/api/manage/reload", { method: "POST" });
  } catch (err) {
    console.error("Failed to reload via /api/manage/reload:", err);
  } finally {
    isReloading.value = false;
  }
};

// Quota Modal state & methods
interface QuotaLimit {
  name: string;
  remaining: number;
  refresh_date?: number;
}

interface ModelUsage {
  model: string;
  remaining: number;
  refresh_date?: number;
  limits?: QuotaLimit[];
}

const showQuotaModal = ref(false);
const quotaLoading = ref(false);
const quotaError = ref("");
const quotas = ref<Record<string, ModelUsage[]>>({});

const fetchQuotas = async () => {
  quotaLoading.value = true;
  quotaError.value = "";
  try {
    const res = await apiFetch("/api/quota");
    if (!res.ok) {
      throw new Error(`Server returned status ${res.status}`);
    }
    const data = await res.json();
    quotas.value = data;
  } catch (err: any) {
    console.error("Failed to fetch quotas:", err);
    quotaError.value = err.message || "Failed to load quota information";
  } finally {
    quotaLoading.value = false;
  }
};

const openQuotaModal = () => {
  showQuotaModal.value = true;
  fetchQuotas();
};

const closeQuotaModal = () => {
  showQuotaModal.value = false;
};

const getProgressClass = (fraction: number) => {
  if (fraction <= 0.2) return "progress-error";
  if (fraction <= 0.5) return "progress-warning";
  return "progress-success";
};

const getTextColorClass = (fraction: number) => {
  if (fraction <= 0.2) return "text-error";
  if (fraction <= 0.5) return "text-warning";
  return "text-success";
};

const formatRefreshDate = (timestamp?: number) => {
  if (!timestamp) return "No reset pending";
  const date = new Date(timestamp * 1000);
  return date.toLocaleString();
};

const getRelativeTime = (timestamp?: number) => {
  if (!timestamp) return "";
  const diffMs = timestamp * 1000 - Date.now();
  if (diffMs <= 0) return "(resets now)";
  const diffSec = Math.floor(diffMs / 1000);
  const hours = Math.floor(diffSec / 3600);
  const minutes = Math.floor((diffSec % 3600) / 60);
  if (hours > 24) {
    const days = Math.floor(hours / 24);
    return `(in ${days}d ${hours % 24}h)`;
  }
  if (hours > 0) {
    return `(in ${hours}h ${minutes}m)`;
  }
  return `(in ${minutes}m)`;
};

import { APP_THEMES } from "../themes/terminal";

const daisyUiThemes = computed(() => APP_THEMES.filter((t) => t.group === "DaisyUI Themes"));
const catppuccinThemes = computed(() => APP_THEMES.filter((t) => t.group === "Catppuccin Themes"));

onMounted(() => {
  const saved = localStorage.getItem("theme");
  if (saved && APP_THEMES.some((t) => t.id === saved)) {
    currentTheme.value = saved;
  } else {
    const docTheme = document.documentElement.getAttribute("data-theme");
    if (docTheme && APP_THEMES.some((t) => t.id === docTheme)) {
      currentTheme.value = docTheme;
    }
  }
  document.documentElement.setAttribute("data-theme", currentTheme.value);
});

const selectTheme = (themeId: string) => {
  currentTheme.value = themeId;
  document.documentElement.setAttribute("data-theme", themeId);
  localStorage.setItem("theme", themeId);

  // Close dropdown by removing focus from active element if focused
  if (document.activeElement instanceof HTMLElement) {
    document.activeElement.blur();
  }
};
</script>

<template>
  <aside
    :class="[
      isOpen
        ? 'w-72 md:w-64 max-md:translate-x-0'
        : 'w-72 md:w-16 md:items-center max-md:-translate-x-full',
      'bg-base-300 border-r border-base-100 flex flex-col h-full shrink-0 transition-all duration-300',
      'max-md:fixed max-md:top-0 max-md:bottom-0 max-md:left-0 max-md:z-50 max-md:shadow-2xl md:relative',
    ]"
  >
    <!-- Header / Toggle Sidebar Button -->
    <div
      :class="['p-4 flex items-center gap-2 w-full', isOpen ? 'justify-between' : 'justify-center']"
    >
      <h1
        v-if="isOpen"
        class="text-lg font-bold bg-gradient-to-r from-indigo-600 to-cyan-600 dark:from-indigo-400 dark:to-cyan-400 bg-clip-text text-transparent truncate"
      >
        Asgard
      </h1>
      <button
        @click="emit('toggle-sidebar')"
        class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
        :title="isOpen ? 'Collapse Sidebar' : 'Expand Sidebar'"
      >
        <Icon icon="mynaui:sidebar" class="h-5 w-5 fill-current" />
      </button>
    </div>

    <!-- New Chat Button (above view mode switch) -->
    <div :class="['px-2 pt-1 pb-1 w-full flex flex-col items-center']">
      <button
        @click="emit('new-chat')"
        :class="[
          'flex items-center gap-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        title="New chat"
      >
        <Icon icon="mynaui:edit-one" class="h-5 w-5 fill-current" />
        <span v-if="isOpen">New chat</span>
      </button>
    </div>

    <!-- View Mode Switcher (List Mode vs Group by Agent Mode) -->
    <div v-if="isOpen" class="px-3 pb-2 w-full">
      <div class="join w-full bg-base-200/60 p-0.5 rounded-lg">
        <button
          @click="toggleViewMode('list')"
          :class="[
            'join-item btn btn-xs flex-1 border-none font-medium gap-1.5',
            viewMode === 'list'
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content',
          ]"
          title="List View Mode"
        >
          <Icon icon="mynaui:list-solid" class="h-4 w-4 fill-current" />
          <span>List</span>
        </button>
        <button
          @click="toggleViewMode('agent')"
          :class="[
            'join-item btn btn-xs flex-1 border-none font-medium gap-1.5',
            viewMode === 'agent'
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content',
          ]"
          title="Group by Agent Model"
        >
          <Icon icon="mynaui:grid" class="h-4 w-4 fill-current" />
          <span>By Agent</span>
        </button>
      </div>
    </div>

    <!-- Sessions List -->
    <div class="flex-1 overflow-y-auto p-2 space-y-1 w-full flex flex-col items-center">
      <template v-if="isOpen">
        <div v-if="sessions.length === 0" class="text-xs text-base-content/50 text-center py-6">
          No active sessions
        </div>

        <!-- 1. List Mode -->
        <template v-else-if="viewMode === 'list'">
          <SessionItem
            v-for="session in sessions"
            :key="session.chatID"
            :session="session"
            :is-active="activeSessionId === session.chatID"
            @select-session="emit('select-session', $event)"
            @delete-session="emit('delete-session', $event)"
          />
        </template>

        <!-- 2. Group by Agent Model Mode -->
        <template v-else-if="viewMode === 'agent'">
          <div
            v-for="(agentSessions, agentName) in groupedSessions"
            :key="agentName"
            class="w-full space-y-1 mb-2"
          >
            <div
              @click="toggleGroupCollapse(agentName)"
              class="px-2 py-1 flex items-center justify-between text-xs font-semibold text-primary/80 uppercase tracking-wider select-none cursor-pointer hover:bg-base-200/50 rounded-md transition-colors"
            >
              <div class="flex items-center gap-1.5 min-w-0">
                <Icon
                  icon="mynaui:chevron-down"
                  :class="[
                    'h-3.5 w-3.5 fill-current shrink-0 transition-transform duration-200',
                    collapsedGroups[agentName] ? '-rotate-90' : '',
                  ]"
                />
                <Icon :icon="getAgentIcon(agentName)" class="h-4 w-4 shrink-0" />
                <span class="truncate">{{ agentName }}</span>
              </div>
              <span class="text-[10px] text-base-content/40 font-normal shrink-0"
                >({{ agentSessions.length }})</span
              >
            </div>

            <template v-if="!collapsedGroups[agentName]">
              <SessionItem
                v-for="session in agentSessions"
                :key="session.chatID"
                :session="session"
                :is-active="activeSessionId === session.chatID"
                @select-session="emit('select-session', $event)"
                @delete-session="emit('delete-session', $event)"
              />
            </template>
          </div>
        </template>
      </template>
    </div>

    <!-- Action Menu (Horizontal with icon only, visible when sidebar is open) -->
    <div
      v-if="isOpen"
      class="px-3 py-1 flex items-center justify-around gap-1 w-full border-t border-base-100/50"
    >
      <button
        @click="reloadApp"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Reload Agents"
        :disabled="isReloading"
      >
        <Icon
          icon="mynaui:refresh"
          :class="['h-5 w-5 fill-current', { 'animate-spin': isReloading }]"
        />
      </button>

      <button
        @click="openQuotaModal"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Check Quota"
      >
        <Icon icon="mynaui:chart-bar-one" class="h-5 w-5 fill-current" />
      </button>

      <button
        @click="emit('toggle-terminal')"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Toggle Global Terminal"
      >
        <Icon icon="mynaui:terminal" class="h-5 w-5 fill-current" />
      </button>
    </div>

    <!-- Footer / Theme Selector (Dropdown Up) -->
    <div
      :class="[
        'p-3 bg-base-300 flex items-center text-xs w-full border-t border-base-100',
        isOpen ? 'justify-between px-4' : 'justify-center',
      ]"
    >
      <div class="dropdown dropdown-top w-full">
        <div
          tabindex="0"
          role="button"
          class="btn btn-ghost btn-xs w-full flex items-center justify-between px-2 text-xs text-base-content/80 hover:text-base-content"
          title="Select Theme"
        >
          <div class="flex items-center gap-2 truncate">
            <Icon icon="mynaui:palette" class="w-4 h-4 shrink-0 fill-current text-primary" />
            <span v-if="isOpen" class="truncate font-medium capitalize">
              {{ APP_THEMES.find((t) => t.id === currentTheme)?.name || currentTheme }}
            </span>
          </div>
          <Icon
            v-if="isOpen"
            icon="mynaui:chevron-up"
            class="w-3.5 h-3.5 shrink-0 fill-current opacity-70"
          />
        </div>
        <ul
          tabindex="0"
          class="dropdown-content menu menu-sm bg-base-200 border border-base-100 rounded-box z-50 w-52 p-1.5 shadow-xl max-h-60 overflow-y-auto mb-1"
        >
          <li class="menu-title text-[10px] uppercase font-semibold text-base-content/50 px-2 py-1">
            DaisyUI Themes
          </li>
          <li v-for="t in daisyUiThemes" :key="t.id">
            <button
              @click="selectTheme(t.id)"
              :class="[
                'flex items-center justify-between py-1 px-2 text-xs rounded-md',
                currentTheme === t.id ? 'active font-medium' : '',
              ]"
            >
              <span>{{ t.name }}</span>
              <Icon v-if="currentTheme === t.id" icon="mynaui:check" class="w-4 h-4 shrink-0" />
            </button>
          </li>
          <li
            class="menu-title text-[10px] uppercase font-semibold text-base-content/50 px-2 py-1 mt-1"
          >
            Catppuccin Themes
          </li>
          <li v-for="t in catppuccinThemes" :key="t.id">
            <button
              @click="selectTheme(t.id)"
              :class="[
                'flex items-center justify-between py-1 px-2 text-xs rounded-md',
                currentTheme === t.id ? 'active font-medium' : '',
              ]"
            >
              <span>{{ t.name }}</span>
              <Icon v-if="currentTheme === t.id" icon="mynaui:check" class="w-4 h-4 shrink-0" />
            </button>
          </li>
        </ul>
      </div>
    </div>
  </aside>

  <!-- Quota Modal -->
  <Transition name="fade">
    <div
      v-if="showQuotaModal"
      class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-center justify-center p-4"
      @click.self="closeQuotaModal"
    >
      <div
        class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-xl max-h-[85vh] flex flex-col shadow-2xl overflow-hidden transition-all transform scale-100"
      >
        <!-- Header -->
        <div
          class="px-6 py-4 border-b border-base-100 flex items-center justify-between bg-base-300/50"
        >
          <div class="flex items-center gap-2">
            <Icon icon="mynaui:chart-bar-one" class="h-6 w-6 text-primary" />
            <h2 class="text-lg font-bold text-base-content">Model Quota Details</h2>
          </div>
          <button
            @click="closeQuotaModal"
            class="btn btn-ghost btn-sm btn-square text-base-content/70 hover:text-base-content hover:bg-base-100/50"
          >
            <Icon icon="mynaui:x" class="h-5 w-5 fill-current" />
          </button>
        </div>

        <!-- Body -->
        <div class="p-6 overflow-y-auto flex-1 space-y-6">
          <div
            v-if="quotaLoading"
            class="flex flex-col items-center justify-center py-12 space-y-3"
          >
            <span class="loading loading-spinner loading-lg text-primary"></span>
            <span class="text-sm text-base-content/70">Fetching current quota data...</span>
          </div>

          <div v-else-if="quotaError" class="alert alert-error flex items-start gap-3">
            <Icon icon="mynaui:danger" class="h-6 w-6 shrink-0" />
            <div>
              <h3 class="font-bold">Error loading quota</h3>
              <div class="text-xs">{{ quotaError }}</div>
            </div>
          </div>

          <div v-else class="space-y-6">
            <div v-for="(models, cliName) in quotas" :key="cliName" class="space-y-3">
              <div class="flex items-center gap-2 border-b border-base-100/60 pb-1.5">
                <span class="text-xs font-bold uppercase tracking-wider text-primary/80">CLI:</span>
                <span
                  class="text-sm font-semibold capitalize bg-primary/10 text-primary px-2.5 py-0.5 rounded-full"
                  >{{ cliName }}</span
                >
              </div>

              <div class="space-y-4">
                <div
                  v-for="m in models"
                  :key="m.model"
                  class="bg-base-300/40 border border-base-100/30 rounded-xl p-4 space-y-3"
                >
                  <div class="flex justify-between items-start">
                    <h4 class="font-medium text-sm text-base-content">{{ m.model }}</h4>
                    <span
                      class="text-xs font-semibold px-2 py-0.5 rounded-md"
                      :class="[
                        m.remaining <= 0.2
                          ? 'bg-error/10 text-error'
                          : m.remaining <= 0.5
                            ? 'bg-warning/10 text-warning'
                            : 'bg-success/10 text-success',
                      ]"
                    >
                      {{ Math.round(m.remaining * 100) }}% remaining
                    </span>
                  </div>

                  <!-- Single Progress Bar (when no multi-tier breakdown limits exist) -->
                  <div v-if="!m.limits || m.limits.length === 0" class="space-y-1">
                    <progress
                      class="progress w-full"
                      :class="getProgressClass(m.remaining)"
                      :value="m.remaining * 100"
                      max="100"
                    ></progress>
                    <div class="flex justify-between text-[11px] text-base-content/50">
                      <span>0%</span>
                      <span v-if="m.refresh_date" class="italic text-right truncate max-w-[80%]">
                        Resets {{ formatRefreshDate(m.refresh_date) }}
                        {{ getRelativeTime(m.refresh_date) }}
                      </span>
                      <span>100%</span>
                    </div>
                  </div>

                  <!-- Specific Detailed Limits (if any) -->
                  <div v-else class="space-y-2">
                    <h5 class="text-[11px] font-bold uppercase tracking-wider text-base-content/40">
                      Quota Limits Breakdown
                    </h5>
                    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                      <div
                        v-for="lim in m.limits"
                        :key="lim.name"
                        class="bg-base-200/50 border border-base-100/20 rounded-lg p-2.5 space-y-1.5"
                      >
                        <div class="flex justify-between items-center">
                          <span class="text-xs font-semibold text-base-content/80 capitalize">{{
                            lim.name
                          }}</span>
                          <span
                            class="text-[11px] font-medium"
                            :class="getTextColorClass(lim.remaining)"
                          >
                            {{ Math.round(lim.remaining * 100) }}%
                          </span>
                        </div>
                        <progress
                          class="progress progress-xs w-full"
                          :class="getProgressClass(lim.remaining)"
                          :value="lim.remaining * 100"
                          max="100"
                        ></progress>
                        <div
                          v-if="lim.refresh_date"
                          class="text-[9px] text-base-content/40 truncate"
                        >
                          Reset: {{ formatRefreshDate(lim.refresh_date) }}
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div
              v-if="Object.keys(quotas).length === 0"
              class="text-center py-8 text-base-content/50 text-sm"
            >
              No quota information returned from CLI.
            </div>
          </div>
        </div>

        <!-- Footer -->
        <div class="px-6 py-4 border-t border-base-100 flex justify-between bg-base-300/30">
          <button
            @click="fetchQuotas"
            class="btn btn-outline btn-sm gap-2"
            :disabled="quotaLoading"
          >
            <Icon
              icon="mynaui:refresh"
              :class="['h-4 w-4 fill-current', { 'animate-spin': quotaLoading }]"
            />
            Refresh
          </button>
          <button @click="closeQuotaModal" class="btn btn-primary btn-sm">Close</button>
        </div>
      </div>
    </div>
  </Transition>
</template>
