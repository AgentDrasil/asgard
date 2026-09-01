<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { Icon } from "@iconify/vue";
import type { A2UIManifest, A2UITab } from "../../types/a2ui";
import A2UIKpiGrid from "./A2UIKpiGrid.vue";
import A2UIWidgetHost from "./A2UIWidgetHost.vue";

const props = defineProps<{
  manifest: A2UIManifest;
  sessionId?: string;
  manifestPath?: string | null;
}>();

const activeTabId = ref<string>("");

const tabsList = computed<A2UITab[]>(() => {
  return props.manifest?.tabs || [];
});

const iconMap: Record<string, string> = {
  layers: "material-symbols:layers",
  activity: "material-symbols:show-chart",
  table: "octicon:table-24",
  "file-text": "octicon:file-code-24",
  database: "material-symbols:database",
  "pie-chart": "material-symbols:pie-chart",
  "line-chart": "material-symbols:ssid-chart",
  shield: "material-symbols:shield",
  coins: "material-symbols:monetization-on",
  wallet: "material-symbols:account-balance-wallet",
};

function getTabIcon(name?: string): string {
  if (!name) return "material-symbols:layers";
  return iconMap[name.toLowerCase()] || (name.includes(":") ? name : `material-symbols:${name}`);
}

watch(
  tabsList,
  (newTabs) => {
    if (newTabs && newTabs.length > 0) {
      if (!activeTabId.value || !newTabs.some((t) => t.id === activeTabId.value)) {
        activeTabId.value = newTabs[0].id;
      }
    }
  },
  { immediate: true },
);

function getActiveTab(): A2UITab | undefined {
  return tabsList.value.find((t) => t.id === activeTabId.value) || tabsList.value[0];
}
</script>

<template>
  <div class="a2ui-dashboard-container w-full space-y-6 pb-6 select-text">
    <template v-if="manifest">
      <!-- Optional Header (Title, Subtitle, asOfDate) -->
      <div
        v-if="manifest.title || manifest.subtitle || manifest.asOfDate"
        class="flex flex-col sm:flex-row sm:items-center justify-between pb-3 border-b border-base-300 gap-2"
      >
        <div>
          <h2 class="text-base sm:text-lg font-bold text-base-content flex items-center gap-2">
            <Icon
              icon="material-symbols:dashboard-customize-outline"
              class="w-5 h-5 text-primary"
            />
            {{ manifest.title }}
          </h2>
          <p v-if="manifest.subtitle" class="text-xs text-base-content/60 mt-0.5">
            {{ manifest.subtitle }}
          </p>
        </div>

        <div
          v-if="manifest.asOfDate"
          class="text-[11px] font-mono px-2.5 py-1 rounded-md bg-base-300/80 text-base-content/70 border border-base-300 self-start sm:self-auto shrink-0"
        >
          As of: {{ manifest.asOfDate }}
        </div>
      </div>

      <!-- Top Declarative KPI Cards Grid -->
      <A2UIKpiGrid v-if="manifest.kpis && manifest.kpis.length > 0" :kpis="manifest.kpis" />

      <!-- Dynamic Tabs Bar -->
      <div
        v-if="tabsList && tabsList.length > 0"
        class="bg-base-200/80 p-1 rounded-xl border border-base-300 flex flex-wrap gap-1"
      >
        <button
          v-for="tab in tabsList"
          :key="tab.id"
          @click="activeTabId = tab.id"
          class="btn btn-xs sm:btn-sm rounded-lg transition-all flex items-center gap-1.5 font-semibold"
          :class="
            activeTabId === tab.id
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content'
          "
        >
          <Icon :icon="getTabIcon(tab.icon)" class="w-4 h-4" />
          {{ tab.label }}
        </button>
      </div>

      <!-- Active Tab Widgets Container -->
      <div v-if="getActiveTab()" class="space-y-6">
        <!-- Grid-2 Layout -->
        <div
          v-if="getActiveTab()?.layout === 'grid-2'"
          class="grid grid-cols-1 lg:grid-cols-2 gap-5"
        >
          <div
            v-for="w in getActiveTab()?.widgets"
            :key="w.id"
            :class="w.colSpan === 2 ? 'lg:col-span-2' : ''"
          >
            <A2UIWidgetHost
              :widget="w"
              :sessionId="sessionId"
              :manifestPath="manifestPath"
            />
          </div>
        </div>

        <!-- Grid-3 Layout -->
        <div
          v-else-if="getActiveTab()?.layout === 'grid-3'"
          class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5"
        >
          <div
            v-for="w in getActiveTab()?.widgets"
            :key="w.id"
            :class="
              w.colSpan === 3
                ? 'lg:col-span-3 md:col-span-2'
                : w.colSpan === 2
                  ? 'lg:col-span-2 md:col-span-2'
                  : ''
            "
          >
            <A2UIWidgetHost
              :widget="w"
              :sessionId="sessionId"
              :manifestPath="manifestPath"
            />
          </div>
        </div>

        <!-- Stacked Layout (Default) -->
        <div v-else class="space-y-5">
          <div v-for="w in getActiveTab()?.widgets" :key="w.id">
            <A2UIWidgetHost
              :widget="w"
              :sessionId="sessionId"
              :manifestPath="manifestPath"
            />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
