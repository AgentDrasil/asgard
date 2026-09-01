<script setup lang="ts">
import type { A2UIKeyValListWidget } from "../../types/a2ui";
import { formatA2UIMoney, formatA2UIPercent, formatA2UINumber } from "../../utils/a2uiUtils";

defineProps<{
  widget: A2UIKeyValListWidget;
}>();

function formatVal(item: any): string {
  if (item.format === "currency") {
    return formatA2UIMoney(item.value);
  }
  if (item.format === "percent") {
    return formatA2UIPercent(item.value);
  }
  if (item.format === "number") {
    return formatA2UINumber(item.value);
  }
  return String(item.value ?? "");
}

const colorClassMap: Record<string, string> = {
  primary: "bg-primary",
  secondary: "bg-secondary",
  accent: "bg-accent",
  emerald: "bg-emerald-500",
  amber: "bg-amber-500",
  purple: "bg-purple-500",
  sky: "bg-sky-500",
  rose: "bg-rose-500",
};
</script>

<template>
  <div class="card bg-base-200/80 border border-base-300 p-5 sm:p-6 shadow-xs space-y-4 rounded-xl">
    <div v-if="widget.title" class="pb-2 border-b border-base-300">
      <h3 class="text-sm sm:text-base font-bold text-base-content">{{ widget.title }}</h3>
      <p v-if="widget.description" class="text-xs text-base-content/60 mt-0.5">
        {{ widget.description }}
      </p>
    </div>

    <div class="space-y-3.5">
      <div v-for="item in widget.items" :key="item.label" class="space-y-1">
        <div class="flex justify-between text-xs">
          <span class="text-base-content/80 font-medium">{{ item.label }}</span>
          <span class="font-mono text-base-content font-bold">
            {{ formatVal(item) }}
            <span v-if="item.subtext" class="text-base-content/50 font-normal ml-1 font-sans"
              >({{ item.subtext }})</span
            >
          </span>
        </div>

        <div
          v-if="item.progressPct != null"
          class="w-full bg-base-300 h-2 rounded-full overflow-hidden"
        >
          <div
            class="h-full rounded-full transition-all duration-500"
            :class="colorClassMap[item.color || 'primary'] || 'bg-primary'"
            :style="{ width: `${Math.min(100, Math.max(0, item.progressPct))}%` }"
          ></div>
        </div>
      </div>
    </div>
  </div>
</template>
