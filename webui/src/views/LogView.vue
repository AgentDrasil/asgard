<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { useToast } from "../composables/useToast";
import { getSystemLogs, isAuthEnabled } from "../lib/api";
import { useUserProfile } from "../composables/useUserProfile";
import { formatRelativeTime } from "../i18n/timeUtils";
import type { SystemLogEntry } from "../types";

const router = useRouter();
const { t } = useI18n();
const toast = useToast();
const { toastHistory, clearToastHistory } = toast;

const authEnabled = isAuthEnabled();
const { profile, load: loadProfile } = useUserProfile();
const isAdmin = computed(() => !authEnabled || Boolean(profile.value?.isAdmin));

interface UnifiedLogItem {
  id: string;
  timestamp: number;
  timeString: string;
  level: "error" | "warn" | "info";
  component: "backend" | "runner" | "ui";
  nodeId?: string;
  source: string;
  title?: string;
  message: string;
  details?: string;
}

const backendLogs = ref<SystemLogEntry[]>([]);
const isLoading = ref(false);
const searchQuery = ref("");
const selectedLevel = ref<"all" | "error" | "warn">("all");
const selectedComponent = ref<"all" | "backend" | "runner" | "ui">("all");
const copiedId = ref<string | null>(null);
let copyTimeoutId: ReturnType<typeof setTimeout> | null = null;

const fetchLogs = async () => {
  if (!isAdmin.value) {
    backendLogs.value = [];
    return;
  }
  isLoading.value = true;
  try {
    const logs = await getSystemLogs();
    backendLogs.value = logs;
  } catch (err: any) {
    backendLogs.value = [];
    toast.error(err?.message || "Failed to fetch logs");
  } finally {
    isLoading.value = false;
  }
};

watch(isAdmin, (admin) => {
  if (admin) {
    void fetchLogs();
  }
});

onMounted(() => {
  void loadProfile();
  if (isAdmin.value) {
    void fetchLogs();
  }
});

onUnmounted(() => {
  if (copyTimeoutId !== null) {
    clearTimeout(copyTimeoutId);
    copyTimeoutId = null;
  }
});

const formatTime = (ts: number): string => {
  const d = new Date(ts);
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
};

// Merge backend diagnostic logs and frontend toast history
const unifiedLogs = computed<UnifiedLogItem[]>(() => {
  const list: UnifiedLogItem[] = [];

  // Backend diagnostic logs
  for (const log of backendLogs.value) {
    const ts = new Date(log.timestamp).getTime() || Date.now();
    const lvl: "error" | "warn" | "info" =
      log.level === "error" ? "error" : log.level === "warn" ? "warn" : "info";
    const comp: "backend" | "runner" | "ui" =
      log.component === "runner" ? "runner" : log.component === "ui" ? "ui" : "backend";
    list.push({
      id: `backend-${log.id}`,
      timestamp: ts,
      timeString: formatTime(ts),
      level: lvl,
      component: comp,
      nodeId: log.node_id,
      source: log.source || "backend",
      message: log.message,
      details: log.details,
    });
  }

  // Frontend toast history (warning and error popups)
  for (const toast of toastHistory.value) {
    const ts = toast.timestamp || Date.now();
    const lvl: "error" | "warn" | "info" =
      toast.type === "error" ? "error" : toast.type === "warning" ? "warn" : "info";
    list.push({
      id: `toast-${toast.id}`,
      timestamp: ts,
      timeString: formatTime(ts),
      level: lvl,
      component: "ui",
      source: "toast",
      title: toast.title,
      message: toast.message,
    });
  }

  // Sort descending by timestamp (newest first)
  list.sort((a, b) => b.timestamp - a.timestamp);
  return list;
});

// Filter by level, component and search query
const filteredLogs = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  return unifiedLogs.value.filter((item) => {
    // Component filter
    if (selectedComponent.value !== "all" && item.component !== selectedComponent.value) {
      return false;
    }
    // Level filter
    if (selectedLevel.value !== "all" && item.level !== selectedLevel.value) {
      return false;
    }
    // Search query
    if (q) {
      const inMsg = item.message.toLowerCase().includes(q);
      const inSrc = item.source.toLowerCase().includes(q);
      const inComp = item.component.toLowerCase().includes(q);
      const inNode = item.nodeId ? item.nodeId.toLowerCase().includes(q) : false;
      const inTitle = item.title ? item.title.toLowerCase().includes(q) : false;
      const inDetails = item.details ? item.details.toLowerCase().includes(q) : false;
      return inMsg || inSrc || inComp || inNode || inTitle || inDetails;
    }
    return true;
  });
});

const counts = computed(() => {
  let total = 0;
  let errors = 0;
  let warnings = 0;
  let backend = 0;
  let runner = 0;
  let ui = 0;
  for (const item of unifiedLogs.value) {
    total++;
    if (item.level === "error") errors++;
    else if (item.level === "warn") warnings++;

    if (item.component === "backend") backend++;
    else if (item.component === "runner") runner++;
    else if (item.component === "ui") ui++;
  }
  return { total, errors, warnings, backend, runner, ui };
});

const totalCount = computed(() => counts.value.total);
const errorCount = computed(() => counts.value.errors);
const warnCount = computed(() => counts.value.warnings);
const backendCount = computed(() => counts.value.backend);
const runnerCount = computed(() => counts.value.runner);
const uiCount = computed(() => counts.value.ui);

const handleCopyLog = async (item: UnifiedLogItem) => {
  const compLabel =
    item.component === "runner" && item.nodeId
      ? `RUNNER:${item.nodeId}`
      : item.component.toUpperCase();
  const content = [
    `[${item.timeString}] [${item.level.toUpperCase()}] [${compLabel}] [${item.source}]`,
    item.title ? `Title: ${item.title}` : null,
    `Message: ${item.message}`,
    item.details ? `Details:\n${item.details}` : null,
  ]
    .filter(Boolean)
    .join("\n");

  try {
    await navigator.clipboard.writeText(content);
    copiedId.value = item.id;
    if (copyTimeoutId !== null) {
      clearTimeout(copyTimeoutId);
    }
    copyTimeoutId = setTimeout(() => {
      if (copiedId.value === item.id) copiedId.value = null;
      copyTimeoutId = null;
    }, 2000);
  } catch (err) {
    console.error("Failed to copy log content:", err);
  }
};

const handleClear = () => {
  clearToastHistory();
  void fetchLogs();
};

const navigateBackToSettings = () => {
  if (window.history.state?.back) {
    router.back();
  } else {
    router.push("/settings");
  }
};
</script>

<template>
  <div class="flex flex-col h-full w-full bg-base-100 overflow-y-auto">
    <!-- Top Navigation Header -->
    <header
      class="sticky top-0 z-20 flex items-center justify-between border-b border-base-300 bg-base-100/90 px-4 py-3 backdrop-blur md:px-6"
    >
      <div class="flex items-center gap-3">
        <button
          @click="navigateBackToSettings"
          class="btn btn-ghost btn-sm btn-square"
          :title="t('logs.backToSettings')"
          :aria-label="t('logs.backToSettings')"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2">
          <Icon icon="material-symbols:receipt-long-outline" class="w-5 h-5 text-primary" />
          <h1 class="text-base font-semibold md:text-lg">{{ t("logs.title") }}</h1>
        </div>
      </div>

      <div v-if="isAdmin" class="flex items-center gap-2">
        <button
          @click="fetchLogs"
          class="btn btn-ghost btn-sm gap-1.5"
          :disabled="isLoading"
          :title="t('logs.refreshTooltip')"
        >
          <Icon
            icon="material-symbols:refresh"
            class="w-4 h-4"
            :class="{ 'animate-spin': isLoading }"
          />
          <span class="hidden sm:inline">{{ t("logs.refresh") }}</span>
        </button>
        <button
          @click="handleClear"
          class="btn btn-ghost btn-sm text-error gap-1.5"
          :title="t('logs.clearToastHistory')"
        >
          <Icon icon="material-symbols:delete-outline" class="w-4 h-4" />
          <span class="hidden sm:inline">{{ t("logs.clearToastHistory") }}</span>
        </button>
      </div>
    </header>

    <!-- Access Denied for Non-Admin -->
    <div
      v-if="!isAdmin"
      class="flex-1 flex flex-col items-center justify-center p-6 text-center space-y-4 max-w-md mx-auto"
      data-testid="logs-access-denied"
    >
      <div class="p-3.5 rounded-full bg-warning/10 text-warning">
        <Icon icon="mynaui:lock" class="w-8 h-8" />
      </div>
      <div class="space-y-1">
        <h2 class="text-base font-bold text-base-content">{{ t("logs.adminOnlyTitle") }}</h2>
        <p class="text-xs text-base-content/70 leading-relaxed">{{ t("logs.adminOnlyDesc") }}</p>
      </div>
      <button @click="navigateBackToSettings" class="btn btn-outline btn-sm">
        {{ t("logs.backToSettings") }}
      </button>
    </div>

    <!-- Main Content for Admin -->
    <div v-else class="p-4 md:p-6 max-w-6xl w-full mx-auto space-y-5 flex-1">
      <!-- Metrics Overview -->
      <div class="grid grid-cols-3 gap-3 md:gap-4">
        <div class="rounded-lg border border-base-300 bg-base-200/50 p-3 md:p-4">
          <div class="text-xs text-base-content/60 font-medium">{{ t("logs.metrics.total") }}</div>
          <div class="mt-1 text-xl md:text-2xl font-bold">{{ totalCount }}</div>
        </div>
        <div class="rounded-lg border border-error/30 bg-error/5 p-3 md:p-4">
          <div class="text-xs text-error font-medium flex items-center gap-1">
            <Icon icon="material-symbols:error-outline" class="w-3.5 h-3.5" />
            {{ t("logs.metrics.errors") }}
          </div>
          <div class="mt-1 text-xl md:text-2xl font-bold text-error">{{ errorCount }}</div>
        </div>
        <div class="rounded-lg border border-warning/30 bg-warning/5 p-3 md:p-4">
          <div class="text-xs text-warning font-medium flex items-center gap-1">
            <Icon icon="material-symbols:warning-amber-outline" class="w-3.5 h-3.5" />
            {{ t("logs.metrics.warnings") }}
          </div>
          <div class="mt-1 text-xl md:text-2xl font-bold text-warning">{{ warnCount }}</div>
        </div>
      </div>

      <!-- Filters & Search Toolbar -->
      <div class="flex flex-col md:flex-row gap-3 items-stretch md:items-center justify-between">
        <div class="flex flex-wrap items-center gap-2">
          <!-- Component Tabs -->
          <div class="join border border-base-300 rounded-lg p-0.5 bg-base-200">
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium"
              :class="
                selectedComponent === 'all' ? 'btn-active bg-base-100 shadow-xs' : 'btn-ghost'
              "
              @click="selectedComponent = 'all'"
            >
              {{ t("logs.components.all") }}
            </button>
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium"
              :class="
                selectedComponent === 'backend'
                  ? 'btn-active bg-base-100 shadow-xs text-info'
                  : 'btn-ghost'
              "
              @click="selectedComponent = 'backend'"
            >
              {{ t("logs.components.backend", { count: backendCount }) }}
            </button>
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium"
              :class="
                selectedComponent === 'runner'
                  ? 'btn-active bg-base-100 shadow-xs text-secondary'
                  : 'btn-ghost'
              "
              @click="selectedComponent = 'runner'"
            >
              {{ t("logs.components.runner", { count: runnerCount }) }}
            </button>
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium"
              :class="
                selectedComponent === 'ui'
                  ? 'btn-active bg-base-100 shadow-xs text-accent'
                  : 'btn-ghost'
              "
              @click="selectedComponent = 'ui'"
            >
              {{ t("logs.components.ui", { count: uiCount }) }}
            </button>
          </div>

          <!-- Level Tabs -->
          <div class="join border border-base-300 rounded-lg p-0.5 bg-base-200">
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium"
              :class="selectedLevel === 'all' ? 'btn-active bg-base-100 shadow-xs' : 'btn-ghost'"
              @click="selectedLevel = 'all'"
            >
              {{ t("logs.levels.all", { count: totalCount }) }}
            </button>
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium text-error"
              :class="selectedLevel === 'error' ? 'btn-active bg-base-100 shadow-xs' : 'btn-ghost'"
              @click="selectedLevel = 'error'"
            >
              {{ t("logs.levels.error", { count: errorCount }) }}
            </button>
            <button
              class="join-item btn btn-xs md:btn-sm border-0 font-medium text-warning"
              :class="selectedLevel === 'warn' ? 'btn-active bg-base-100 shadow-xs' : 'btn-ghost'"
              @click="selectedLevel = 'warn'"
            >
              {{ t("logs.levels.warn", { count: warnCount }) }}
            </button>
          </div>
        </div>

        <!-- Search Bar -->
        <div class="relative flex-1 md:max-w-xs">
          <Icon
            icon="material-symbols:search"
            class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-base-content/50"
          />
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="t('logs.searchPlaceholder')"
            class="input input-sm w-full pl-9 pr-8 bg-base-200 border-base-300 rounded-lg text-sm"
          />
          <button
            v-if="searchQuery"
            @click="searchQuery = ''"
            class="absolute right-2.5 top-1/2 -translate-y-1/2 text-base-content/50 hover:text-base-content"
          >
            <Icon icon="material-symbols:close" class="w-3.5 h-3.5" />
          </button>
        </div>
      </div>

      <!-- Log Entries List -->
      <div v-if="filteredLogs.length > 0" class="space-y-3">
        <div
          v-for="item in filteredLogs"
          :key="item.id"
          class="rounded-lg border border-base-300 bg-base-200/40 p-3.5 md:p-4 hover:border-base-content/20 transition-colors"
        >
          <!-- Log Item Header -->
          <div class="flex items-center justify-between gap-2 mb-2 flex-wrap">
            <div class="flex items-center gap-2 flex-wrap">
              <!-- Level Badge -->
              <span
                v-if="item.level === 'error'"
                class="badge badge-sm badge-error text-error-content font-bold uppercase"
              >
                {{ t("logs.badges.error") }}
              </span>
              <span
                v-else-if="item.level === 'warn'"
                class="badge badge-sm badge-warning text-warning-content font-bold uppercase"
              >
                {{ t("logs.badges.warn") }}
              </span>
              <span v-else class="badge badge-sm badge-info font-bold uppercase">
                {{ t("logs.badges.info") }}
              </span>

              <!-- Component Badge -->
              <span
                v-if="item.component === 'backend'"
                class="badge badge-sm badge-info badge-outline font-semibold gap-1 text-xs"
              >
                <Icon icon="material-symbols:dns-outline" class="w-3.5 h-3.5" />
                Backend
              </span>
              <span
                v-else-if="item.component === 'runner'"
                class="badge badge-sm badge-secondary badge-outline font-semibold gap-1 text-xs"
              >
                <Icon icon="material-symbols:terminal" class="w-3.5 h-3.5" />
                {{ item.nodeId ? `Runner: ${item.nodeId}` : "Runner" }}
              </span>
              <span
                v-else
                class="badge badge-sm badge-accent badge-outline font-semibold gap-1 text-xs"
              >
                <Icon icon="material-symbols:desktop-windows-outline" class="w-3.5 h-3.5" />
                UI
              </span>

              <!-- Source / Module Badge -->
              <span class="badge badge-sm badge-neutral font-mono text-xs opacity-80">
                {{ item.source }}
              </span>

              <!-- Timestamp -->
              <span class="text-xs text-base-content/60 font-mono" :title="item.timeString">
                {{ item.timeString }}
              </span>
              <span class="text-xs text-base-content/40"
                >({{ formatRelativeTime(item.timestamp, t) }})</span
              >
            </div>

            <!-- Action Copy Button -->
            <button
              @click="handleCopyLog(item)"
              class="btn btn-ghost btn-xs gap-1 text-base-content/60 hover:text-base-content"
              :title="copiedId === item.id ? t('logs.copiedTooltip') : t('logs.copyTooltip')"
            >
              <Icon
                :icon="
                  copiedId === item.id
                    ? 'material-symbols:check'
                    : 'material-symbols:content-copy-outline'
                "
                class="w-3.5 h-3.5"
                :class="{ 'text-success': copiedId === item.id }"
              />
              <span class="text-xs">{{
                copiedId === item.id ? t("logs.copied") : t("logs.copy")
              }}</span>
            </button>
          </div>

          <!-- Title if present -->
          <div v-if="item.title" class="font-semibold text-sm mb-1 text-base-content">
            {{ item.title }}
          </div>

          <!-- Main Message -->
          <div
            class="text-sm font-mono text-base-content/90 whitespace-pre-wrap break-words leading-relaxed select-text"
          >
            {{ item.message }}
          </div>

          <!-- Extra Details if present -->
          <details
            v-if="item.details"
            class="collapse collapse-arrow bg-base-300/40 border border-base-300 rounded-md mt-2"
          >
            <summary
              class="collapse-title text-xs font-semibold py-1.5 min-h-0 text-base-content/70"
            >
              {{ t("logs.details") }}
            </summary>
            <div
              class="collapse-content text-xs font-mono whitespace-pre-wrap break-all text-base-content/80 pt-1 select-text"
            >
              {{ item.details }}
            </div>
          </details>
        </div>
      </div>

      <!-- Empty State -->
      <div
        v-else
        class="flex flex-col items-center justify-center py-16 text-center text-base-content/50 border border-dashed border-base-300 rounded-xl"
      >
        <Icon
          :icon="
            searchQuery || selectedLevel !== 'all' || selectedComponent !== 'all'
              ? 'material-symbols:filter-list-off'
              : 'material-symbols:check-circle-outline'
          "
          class="w-12 h-12 mb-3"
          :class="
            searchQuery || selectedLevel !== 'all' || selectedComponent !== 'all'
              ? 'text-base-content/40'
              : 'text-success/60'
          "
        />
        <div class="text-base font-medium text-base-content/80">{{ t("logs.empty.title") }}</div>
        <div class="text-sm text-base-content/50 mt-1 max-w-sm">
          {{
            searchQuery || selectedLevel !== "all" || selectedComponent !== "all"
              ? t("logs.empty.filtered")
              : t("logs.empty.healthy")
          }}
        </div>
      </div>
    </div>
  </div>
</template>
