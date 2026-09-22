<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { getCompressionMetrics } from "../lib/api";
import { formatFileSize, humanfriendly } from "../lib/format";
import type { CompressionMetrics } from "../types";

const router = useRouter();
const { t } = useI18n();

const metrics = ref<CompressionMetrics | null>(null);
const isLoading = ref(false);

const fetchMetrics = async () => {
  isLoading.value = true;
  try {
    metrics.value = await getCompressionMetrics();
  } finally {
    isLoading.value = false;
  }
};

onMounted(() => {
  void fetchMetrics();
});

interface StatTile {
  key: string;
  label: string;
  value: string;
}

interface StatGroup {
  key: string;
  icon: string;
  title: string;
  description: string;
  tiles: StatTile[];
}

const groups = computed<StatGroup[]>(() => {
  const m = metrics.value;
  return [
    {
      key: "jev",
      icon: "mynaui:sparkles",
      title: t("compressionStats.jev.title"),
      description: t("compressionStats.jev.description"),
      tiles: [
        {
          key: "calls",
          label: t("compressionStats.jev.calls"),
          value: humanfriendly(m?.jev_calls ?? 0),
        },
        {
          key: "tokens",
          label: t("compressionStats.jev.tokens"),
          value: humanfriendly(m?.jev_tokens ?? 0),
        },
        {
          key: "avgTokens",
          label: t("compressionStats.jev.avgTokens"),
          value: (m?.jev_avg_tokens ?? 0).toFixed(1),
        },
      ],
    },
    {
      key: "compass",
      icon: "mynaui:compass",
      title: t("compressionStats.compass.title"),
      description: t("compressionStats.compass.description"),
      tiles: [
        {
          key: "calls",
          label: t("compressionStats.compass.calls"),
          value: humanfriendly(m?.compass_calls ?? 0),
        },
        {
          key: "tokens",
          label: t("compressionStats.compass.tokens"),
          value: humanfriendly(m?.compass_tokens ?? 0),
        },
        {
          key: "avgTokens",
          label: t("compressionStats.compass.avgTokens"),
          value: (m?.compass_avg_tokens ?? 0).toFixed(1),
        },
      ],
    },
    {
      key: "output",
      icon: "mynaui:layers-three",
      title: t("compressionStats.output.title"),
      description: t("compressionStats.output.description"),
      tiles: [
        {
          key: "truncations",
          label: t("compressionStats.output.truncations"),
          value: humanfriendly(m?.truncations ?? 0),
        },
        {
          key: "showOutputCalls",
          label: t("compressionStats.output.showOutputCalls"),
          value: humanfriendly(m?.show_output_calls ?? 0),
        },
        {
          key: "showOutputBytes",
          label: t("compressionStats.output.showOutputBytes"),
          value: formatFileSize(m?.show_output_bytes ?? 0),
        },
      ],
    },
  ];
});

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
          :title="t('compressionStats.backToSettings')"
          :aria-label="t('compressionStats.backToSettings')"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2">
          <Icon icon="mynaui:chart-bar-one" class="w-5 h-5 text-primary" />
          <h1 class="text-base font-semibold md:text-lg">{{ t("compressionStats.title") }}</h1>
        </div>
      </div>

      <button
        @click="fetchMetrics"
        class="btn btn-ghost btn-sm gap-1.5"
        :disabled="isLoading"
        :title="t('compressionStats.refreshTooltip')"
      >
        <Icon
          icon="material-symbols:refresh"
          class="w-4 h-4"
          :class="{ 'animate-spin': isLoading }"
        />
        <span class="hidden sm:inline">{{ t("compressionStats.refresh") }}</span>
      </button>
    </header>

    <div class="p-4 md:p-6 max-w-4xl w-full mx-auto space-y-6 flex-1">
      <template v-if="metrics">
        <section v-for="group in groups" :key="group.key" class="space-y-3">
          <div class="space-y-1">
            <h2 class="text-sm font-semibold flex items-center gap-2">
              <Icon :icon="group.icon" class="w-4 h-4 text-primary" />
              <span>{{ group.title }}</span>
            </h2>
            <p class="text-xs text-base-content/70">{{ group.description }}</p>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 md:gap-4">
            <div
              v-for="tile in group.tiles"
              :key="tile.key"
              class="rounded-xl border border-base-300 bg-base-200/50 p-4"
            >
              <div class="text-xs text-base-content/60 font-medium">{{ tile.label }}</div>
              <div class="mt-1 text-xl md:text-2xl font-bold font-mono">{{ tile.value }}</div>
            </div>
          </div>
        </section>
      </template>

      <!-- Empty State -->
      <div
        v-else
        class="flex flex-col items-center justify-center py-16 text-center text-base-content/50 border border-dashed border-base-300 rounded-xl"
      >
        <Icon icon="mynaui:chart-bar-one" class="w-12 h-12 text-base-content/30 mb-3" />
        <div class="text-base font-medium text-base-content/80">
          {{ t("compressionStats.empty.title") }}
        </div>
        <div class="text-sm text-base-content/50 mt-1 max-w-sm">
          {{ t("compressionStats.empty.description") }}
        </div>
      </div>
    </div>
  </div>
</template>
