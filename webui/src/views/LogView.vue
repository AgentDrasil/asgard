<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { useToast } from "../composables/useToast";
import { getSystemLogs } from "../lib/api";
import { formatRelativeTime } from "../i18n/timeUtils";
import type { SystemLogEntry } from "../types";

const router = useRouter();
const { t } = useI18n();
const { toastHistory, clearToastHistory } = useToast();

interface UnifiedLogItem {
  id: string;
  timestamp: number;
  timeString: string;
  level: "error" | "warn" | "info";
  source: string;
  title?: string;
  message: string;
  details?: string;
}

const backendLogs = ref<SystemLogEntry[]>([]);
const isLoading = ref(false);
const searchQuery = ref("");
const selectedLevel = ref<"all" | "error" | "warn">("all");
const copiedId = ref<string | null>(null);
let copyTimeoutId: ReturnType<typeof setTimeout> | null = null;

const fetchLogs = async () => {
  isLoading.value = true;
  try {
    const logs = await getSystemLogs();
    backendLogs.value = logs;
  } finally {
    isLoading.value = false;
  }
};

onMounted(() => {
  void fetchLogs();
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
    list.push({
      id: `backend-${log.id}`,
      timestamp: ts,
      timeString: formatTime(ts),
      level: lvl,
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
      source: "toast",
      title: toast.title,
      message: toast.message,
    });
  }

  // Sort descending by timestamp (newest first)
  list.sort((a, b) => b.timestamp - a.timestamp);
  return list;
});

// Filter by level and search query
const filteredLogs = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  return unifiedLogs.value.filter((item) => {
    // Level filter
    if (selectedLevel.value !== "all" && item.level !== selectedLevel.value) {
      return false;
    }
    // Search query
    if (q) {
      const inMsg = item.message.toLowerCase().includes(q);
      const inSrc = item.source.toLowerCase().includes(q);
      const inTitle = item.title ? item.title.toLowerCase().includes(q) : false;
      const inDetails = item.details ? item.details.toLowerCase().includes(q) : false;
      return inMsg || inSrc || inTitle || inDetails;
    }
    return true;
  });
});

const totalCount = computed(() => unifiedLogs.value.length);
const errorCount = computed(() => unifiedLogs.value.filter((l) => l.level === "error").length);
const warnCount = computed(() => unifiedLogs.value.filter((l) => l.level === "warn").length);

const handleCopyLog = async (item: UnifiedLogItem) => {
  const content = [
    `[${item.timeString}] [${item.level.toUpperCase()}] [${item.source}]`,
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

      <div class="flex items-center gap-2">
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

    <div class="p-4 md:p-6 max-w-6xl w-full mx-auto space-y-5 flex-1">
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
      <div class="flex flex-col sm:flex-row gap-3 items-stretch sm:items-center justify-between">
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

        <!-- Search Bar -->
        <div class="relative flex-1 sm:max-w-xs">
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

              <!-- Source Badge -->
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
        <Icon icon="material-symbols:check-circle-outline" class="w-12 h-12 text-success/60 mb-3" />
        <div class="text-base font-medium text-base-content/80">{{ t("logs.empty.title") }}</div>
        <div class="text-sm text-base-content/50 mt-1 max-w-sm">
          {{
            searchQuery || selectedLevel !== "all"
              ? t("logs.empty.filtered")
              : t("logs.empty.healthy")
          }}
        </div>
      </div>
    </div>
  </div>
</template>
