<script setup lang="ts">
import { ref, onMounted } from "vue";
import type { ChatSession } from "../types";
import { Icon } from "@iconify/vue";
import { apiFetch } from "../lib/api";

const props = withDefaults(
  defineProps<{
    sessions: ChatSession[];
    activeSessionId: string | null;
    isOpen?: boolean;
  }>(),
  {
    isOpen: true,
  },
);

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "new-chat"): void;
  (e: "delete-session", id: string): void;
  (e: "toggle-sidebar"): void;
}>();

const currentTheme = ref("dark");
const isReloading = ref(false);

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

onMounted(() => {
  const saved = localStorage.getItem("theme");
  if (saved) {
    currentTheme.value = saved;
  } else {
    const docTheme = document.documentElement.getAttribute("data-theme");
    if (docTheme) {
      currentTheme.value = docTheme;
    }
  }
  document.documentElement.setAttribute("data-theme", currentTheme.value);
});

const toggleTheme = () => {
  currentTheme.value = currentTheme.value === "dark" ? "light" : "dark";
  document.documentElement.setAttribute("data-theme", currentTheme.value);
  localStorage.setItem("theme", currentTheme.value);
};
</script>

<template>
  <aside
    :class="[
      isOpen ? 'w-64' : 'w-16 items-center',
      'bg-base-300 border-r border-base-100 flex flex-col h-full shrink-0 transition-all duration-200',
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
        Asgard WebUI
      </h1>
      <button
        @click="emit('toggle-sidebar')"
        class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
        :title="isOpen ? 'Collapse Sidebar' : 'Expand Sidebar'"
      >
        <Icon icon="mynaui:sidebar" class="h-5 w-5 fill-current" />
      </button>
    </div>

    <!-- Sessions List -->
    <div class="flex-1 overflow-y-auto p-2 space-y-1 w-full flex flex-col items-center">
      <!-- New Chat Button in List -->
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

      <template v-if="isOpen">
        <div v-if="sessions.length === 0" class="text-xs text-base-content/50 text-center py-6">
          No active sessions
        </div>
        <div
          v-for="session in sessions"
          :key="session.chatID"
          @click="emit('select-session', session.chatID)"
          :class="[
            'group flex items-center justify-between px-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium w-full',
            activeSessionId === session.chatID
              ? 'bg-primary text-primary-content shadow-md shadow-primary/10'
              : 'hover:bg-base-200 text-base-content/85',
          ]"
        >
          <span class="truncate pr-2 select-none">{{ session.title || "Untitled Chat" }}</span>

          <button
            @click.stop="emit('delete-session', session.chatID)"
            :class="[
              'btn btn-ghost btn-xs opacity-0 group-hover:opacity-100 transition-opacity p-1 min-h-0 h-6 w-6',
              activeSessionId === session.chatID
                ? 'text-primary-content hover:bg-white/20'
                : 'text-error hover:bg-error/10',
            ]"
            title="Delete session"
          >
            <Icon icon="mynaui:trash-one" class="h-4 w-4 fill-current" />
          </button>
        </div>
      </template>
    </div>

    <!-- Action Menu (Horizontal with icon only) -->
    <div
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

      <a
        href="/api/ttyd/sidebar"
        target="_blank"
        rel="noopener noreferrer"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Open Terminal"
      >
        <Icon icon="mynaui:terminal" class="h-5 w-5 fill-current" />
      </a>
    </div>

    <!-- Footer / Theme Toggle -->
    <div
      :class="[
        'p-3 bg-base-300 flex items-center text-xs w-full border-t border-base-100',
        isOpen ? 'justify-between px-4' : 'justify-center',
      ]"
    >
      <span v-if="isOpen" class="text-base-content/70 font-medium select-none capitalize">
        {{ currentTheme }} mode
      </span>
      <label
        class="swap swap-rotate btn btn-ghost btn-xs btn-circle text-base-content/80 hover:text-base-content"
        title="Toggle Light/Dark Theme"
      >
        <input
          type="checkbox"
          class="theme-controller"
          :checked="currentTheme === 'light'"
          @change="toggleTheme"
        />
        <!-- Sun icon (shown when dark, click for light) -->
        <Icon icon="mynaui:sun" class="swap-off fill-current w-5 h-5" />
        <!-- Moon icon (shown when light, click for dark) -->
        <Icon icon="mynaui:moon" class="swap-on fill-current w-5 h-5" />
      </label>
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

                  <!-- Overall Progress Bar -->
                  <div class="space-y-1">
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
                  <div
                    v-if="m.limits && m.limits.length > 0"
                    class="mt-3 pt-3 border-t border-base-100/40 space-y-2"
                  >
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
