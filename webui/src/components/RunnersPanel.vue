<script setup lang="ts">
import { onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { useRunners } from "../composables/useRunners";
import { formatRelativeTime } from "../i18n/timeUtils";

const { t } = useI18n();
const { runners, backendVersion, loading, refreshRunners } = useRunners();

onMounted(() => {
  void refreshRunners();
});

const lastSeen = (ts?: string) => (ts ? formatRelativeTime(ts, t) : t("runners.never"));
</script>

<template>
  <section class="space-y-3" data-testid="runners-panel">
    <div class="flex items-center justify-between gap-2">
      <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
        {{ t("runners.title") }}
      </h2>
      <button
        class="btn btn-ghost btn-xs gap-1.5"
        :disabled="loading"
        data-testid="runners-refresh"
        @click="refreshRunners"
      >
        <Icon icon="mynaui:refresh" :class="['h-4 w-4', { 'animate-spin': loading }]" />
        <span>{{ t("runners.refresh") }}</span>
      </button>
    </div>
    <div class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 space-y-3">
      <p class="text-xs text-base-content/70">{{ t("runners.desc") }}</p>
      <p v-if="backendVersion" class="text-xs text-base-content/70" data-testid="backend-version">
        {{ t("runners.backendVersion") }}:
        <span class="font-mono text-base-content">{{ backendVersion }}</span>
      </p>
      <p v-if="runners.length === 0" class="text-sm text-base-content/60">
        {{ t("runners.noRunners") }}
      </p>
      <ul v-else class="divide-y divide-base-300">
        <li
          v-for="r in runners"
          :key="r.node_id"
          class="py-2 flex flex-wrap items-center gap-x-4 gap-y-1"
          data-testid="runner-row"
        >
          <span class="flex items-center gap-2 font-medium min-w-0">
            <span
              :class="[
                'inline-block h-2 w-2 rounded-full shrink-0',
                r.online ? 'bg-success' : 'bg-error',
              ]"
            />
            <span class="truncate font-mono">{{ r.node_id }}</span>
            <span :class="['badge badge-xs', r.online ? 'badge-success' : 'badge-error']">{{
              r.online ? t("runners.online") : t("runners.offline")
            }}</span>
          </span>
          <span class="text-xs text-base-content/70">
            {{ t("runners.lastSeen") }}: {{ lastSeen(r.last_seen) }}
          </span>
          <span v-if="r.active_runs !== undefined" class="text-xs text-base-content/70">
            {{ t("runners.activeRuns") }}: {{ r.active_runs }}
          </span>
          <span v-if="r.version" class="text-xs text-base-content/70">
            {{ t("runners.runnerVersion") }}:
            <span class="font-mono text-base-content">{{ r.version }}</span>
          </span>
        </li>
      </ul>
    </div>
  </section>
</template>
