<script setup lang="ts">
import { Icon } from "@iconify/vue";
import type { A2UIKpi } from "../../types/a2ui";
import { formatA2UIMoney, formatA2UIPercent, formatA2UINumber } from "../../utils/a2uiUtils";

defineProps<{
  kpis: A2UIKpi[];
}>();

const iconMap: Record<string, string> = {
  wallet: "material-symbols:account-balance-wallet",
  "pie-chart": "material-symbols:pie-chart",
  "shield-check": "material-symbols:shield-with-heart",
  gauge: "material-symbols:speed",
  coins: "material-symbols:monetization-on",
  percent: "material-symbols:percent",
  layers: "material-symbols:layers",
  activity: "material-symbols:show-chart",
  "trending-up": "material-symbols:trending-up",
  "trending-down": "material-symbols:trending-down",
  dollar: "material-symbols:attach-money",
  table: "octicon:table-24",
  "file-text": "octicon:file-code-24",
};

function getKpiIcon(name?: string): string {
  if (!name) return "material-symbols:attach-money";
  return iconMap[name.toLowerCase()] || (name.includes(":") ? name : `material-symbols:${name}`);
}

function formatValue(kpi: A2UIKpi): string {
  if (kpi.format === "currency") {
    return formatA2UIMoney(kpi.value);
  }
  if (kpi.format === "percent") {
    return formatA2UIPercent(kpi.value);
  }
  if (kpi.format === "number") {
    return formatA2UINumber(kpi.value);
  }
  return String(kpi.value ?? "");
}

const colorClassMap: Record<string, string> = {
  primary: "text-primary",
  secondary: "text-secondary",
  accent: "text-accent",
  emerald: "text-emerald-500 dark:text-emerald-400",
  amber: "text-amber-500 dark:text-amber-400",
  purple: "text-purple-500 dark:text-purple-400",
  sky: "text-sky-500 dark:text-sky-400",
  rose: "text-rose-500 dark:text-rose-400",
};
</script>

<template>
  <div
    v-if="kpis && kpis.length > 0"
    class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3.5"
  >
    <div
      v-for="kpi in kpis"
      :key="kpi.id || kpi.label"
      class="card bg-base-200/80 border border-base-300 shadow-xs p-4 sm:p-5 hover:border-base-content/20 transition-all rounded-xl"
    >
      <div class="flex justify-between items-start">
        <span class="text-[11px] font-semibold uppercase tracking-wider text-base-content/60">{{
          kpi.label
        }}</span>
        <span class="p-2 rounded-lg bg-base-300/80" :class="colorClassMap[kpi.color || 'primary']">
          <Icon :icon="getKpiIcon(kpi.icon)" class="w-4 h-4" />
        </span>
      </div>

      <div class="mt-2.5">
        <div
          class="text-xl sm:text-2xl font-black font-mono tracking-tight"
          :class="colorClassMap[kpi.color || ''] || 'text-base-content'"
        >
          {{ formatValue(kpi) }}
        </div>

        <!-- Optional Change / Badge -->
        <div
          v-if="kpi.change"
          class="mt-1 flex items-center text-xs font-medium"
          :class="kpi.changeType === 'negative' ? 'text-rose-500' : 'text-emerald-500'"
        >
          <Icon
            :icon="
              kpi.changeType === 'negative'
                ? 'material-symbols:trending-down'
                : 'material-symbols:arrow-outward'
            "
            class="w-3.5 h-3.5 mr-1 shrink-0"
          />
          <span>{{ kpi.change }}</span>
          <span v-if="kpi.subtext" class="text-base-content/50 ml-1.5 font-normal truncate">{{
            kpi.subtext
          }}</span>
        </div>

        <!-- Optional Subtext without change -->
        <div v-else-if="kpi.subtext" class="mt-1 text-xs text-base-content/60 truncate">
          {{ kpi.subtext }}
        </div>
      </div>
    </div>
  </div>
</template>
