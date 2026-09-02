<script setup lang="ts">
import { ref, computed, watch, onMounted } from "vue";
import { Icon } from "@iconify/vue";
import Papa from "papaparse";
import { useI18n } from "vue-i18n";
import type { A2UIDataTableWidget, A2UIDataTableColumn } from "../../../types/a2ui";

import { resolveBadgeClass } from "../../../utils/badgeHelper";
import {
  formatA2UIMoney,
  formatA2UIPercent,
  formatA2UINumber,
  fetchWorkspaceAsset,
} from "../../../utils/a2uiUtils";

const { t } = useI18n();

const props = defineProps<{
  widget: A2UIDataTableWidget;
  sessionId?: string;
  manifestPath?: string | null;
}>();

const rawRows = ref<any[]>([]);
const headers = ref<string[]>([]);
const isLoading = ref<boolean>(false);

const searchQuery = ref<string>("");
const activeFilters = ref<Record<string, string>>({});
const sortKey = ref<string>("");
const sortAsc = ref<boolean>(false);

async function loadData() {
  if (props.widget.rows && props.widget.rows.length > 0) {
    rawRows.value = props.widget.rows;
    headers.value = props.widget.headers || Object.keys(props.widget.rows[0] || {});
    return;
  }

  if (props.widget.sourceCsv && props.sessionId) {
    isLoading.value = true;
    try {
      const csvText = await fetchWorkspaceAsset(
        props.sessionId,
        props.widget.sourceCsv,
        props.manifestPath,
      );
      if (csvText) {
        const parsed = Papa.parse<Record<string, any>>(csvText, {
          header: true,
          dynamicTyping: true,
          skipEmptyLines: true,
        });
        headers.value = parsed.meta.fields || [];
        rawRows.value = parsed.data || [];
      }
    } catch (e) {
      console.error("Failed to load sourceCsv for A2UIDataTable:", e);
    } finally {
      isLoading.value = false;
    }
  }
}

onMounted(() => {
  loadData();
});

watch(
  () => [props.widget.sourceCsv, props.widget.rows, props.sessionId, props.manifestPath],
  () => {
    loadData();
  },
);

// Filter Options extraction for declared filters
const filterOptionsMap = computed(() => {
  const map: Record<string, string[]> = {};
  const declaredFilters = props.widget.features?.filters || [];
  for (const f of declaredFilters) {
    if (f.options && f.options.length > 0) {
      map[f.key] = f.options;
    } else {
      const s = new Set<string>();
      rawRows.value.forEach((r) => {
        if (r[f.key] !== undefined && r[f.key] !== null) s.add(String(r[f.key]));
      });
      map[f.key] = Array.from(s);
    }
  }
  return map;
});

function resetFilters() {
  searchQuery.value = "";
  activeFilters.value = {};
}

function toggleSort(key: string) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value;
  } else {
    sortKey.value = key;
    sortAsc.value = true;
  }
}

// Columns definition
const columns = computed<A2UIDataTableColumn[]>(() => {
  if (props.widget.columns && props.widget.columns.length > 0) {
    return props.widget.columns;
  }
  return headers.value.map((h) => ({
    key: h,
    label: h.replace(/_/g, " ").toUpperCase(),
    align: "left" as const,
    format: "text" as const,
    sortable: true,
  }));
});

// Processed Rows (Filtered + Sorted)
const processedRows = computed(() => {
  // 1. Filter
  const filtered = rawRows.value.filter((row) => {
    for (const [k, val] of Object.entries(activeFilters.value)) {
      if (val && String(row[k]) !== val) return false;
    }
    if (searchQuery.value) {
      const q = searchQuery.value.trim().toLowerCase();
      if (q) {
        const matches = Object.values(row).some((v) =>
          String(v ?? "")
            .toLowerCase()
            .includes(q),
        );
        if (!matches) return false;
      }
    }
    return true;
  });

  // 2. Sort
  if (!sortKey.value) return filtered;
  return [...filtered].sort((a, b) => {
    let valA = a[sortKey.value];
    let valB = b[sortKey.value];
    const numA = parseFloat(String(valA || "").replace(/,/g, ""));
    const numB = parseFloat(String(valB || "").replace(/,/g, ""));
    if (!isNaN(numA) && !isNaN(numB)) {
      valA = numA;
      valB = numB;
    } else {
      valA = String(valA || "").toLowerCase();
      valB = String(valB || "").toLowerCase();
    }
    if (valA < valB) return sortAsc.value ? -1 : 1;
    if (valA > valB) return sortAsc.value ? 1 : -1;
    return 0;
  });
});

function getBadgeList(row: any, col: any): string[] {
  const val = row[col.key];
  if (val === undefined || val === null || val === "") return [];
  if (Array.isArray(val)) return val.map((v) => String(v).trim()).filter(Boolean);
  const strVal = String(val);
  if (strVal.includes(",")) {
    return strVal
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
  }
  return [strVal];
}

function formatCell(row: any, col: A2UIDataTableColumn): string {
  const val = row[col.key];
  if (col.format === "currency") {
    return formatA2UIMoney(val);
  }
  if (col.format === "number") {
    return formatA2UINumber(val);
  }
  if (col.format === "percent") {
    return formatA2UIPercent(val);
  }
  return String(val ?? "");
}

function exportCsv() {
  if (!processedRows.value.length) return;
  const cols = columns.value;
  const csvContent = [
    cols.map((c) => `"${c.label}"`).join(","),
    ...processedRows.value.map((r) =>
      cols.map((c) => `"${(r[c.key] ?? "").toString().replace(/"/g, '""')}"`).join(","),
    ),
  ].join("\n");

  const blob = new Blob([csvContent], { type: "text/csv;charset=utf-8;" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${props.widget.title || "export"}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}
</script>

<template>
  <div class="card bg-base-200/80 border border-base-300 shadow-xs p-4 sm:p-5 space-y-3 rounded-xl">
    <!-- Header Block (Title & Description + Export) -->
    <div
      v-if="widget.title || widget.description || widget.features?.exportCsv !== false"
      class="flex flex-col sm:flex-row sm:items-center justify-between gap-2"
    >
      <div>
        <h3
          v-if="widget.title"
          class="text-sm sm:text-base font-bold text-base-content flex items-center gap-2"
        >
          <Icon icon="octicon:table-24" class="w-4 h-4 text-primary" /> {{ widget.title }}
        </h3>
        <p v-if="widget.description" class="text-xs text-base-content/60 mt-0.5">
          {{ widget.description }}
        </p>
      </div>

      <!-- Export CSV Button -->
      <button
        v-if="widget.features?.exportCsv !== false"
        @click="exportCsv"
        class="btn btn-xs sm:btn-sm btn-outline border-base-300 text-base-content/80 hover:text-base-content gap-1 text-xs self-start sm:self-auto shrink-0"
      >
        <Icon icon="octicon:download-16" class="w-3.5 h-3.5 text-primary" />
        <span>{{ t("a2ui.dataTable.exportCsv") }}</span>
      </button>
    </div>

    <!-- Controls Toolbar -->
    <div
      v-if="
        widget.features?.search !== false ||
        (widget.features?.filters && widget.features.filters.length > 0)
      "
      class="flex flex-wrap items-center gap-2 pt-1 pb-3 border-b border-base-300"
    >
      <!-- Search Input -->
      <div v-if="widget.features?.search !== false" class="relative w-full sm:w-48">
        <Icon
          icon="material-symbols:search"
          class="w-4 h-4 absolute left-2.5 top-2 text-base-content/50"
        />
        <input
          v-model="searchQuery"
          type="text"
          :placeholder="
            columns[0]
              ? t('a2ui.dataTable.searchPlaceholder', { label: columns[0].label })
              : t('a2ui.dataTable.searchDefaultPlaceholder')
          "
          class="input input-xs sm:input-sm input-bordered w-full pl-8 rounded-lg bg-base-100 text-xs text-base-content placeholder:text-base-content/40"
        />
      </div>

      <!-- Filter Dropdowns -->
      <template v-if="widget.features?.filters">
        <select
          v-for="f in widget.features.filters"
          :key="f.key"
          :value="activeFilters[f.key] || ''"
          @change="activeFilters[f.key] = ($event.target as HTMLSelectElement).value"
          class="select select-xs sm:select-sm select-bordered bg-base-100 text-xs rounded-lg text-base-content"
          :class="activeFilters[f.key] ? 'border-primary/60 font-semibold' : 'border-base-300'"
        >
          <option value="">{{ t("a2ui.dataTable.allFilter", { label: f.label || f.key }) }}</option>
          <option v-for="opt in filterOptionsMap[f.key] || []" :key="opt" :value="opt">
            {{ opt }}
          </option>
        </select>
      </template>

      <!-- Reset Button -->
      <button
        @click="resetFilters"
        class="btn btn-xs sm:btn-sm btn-ghost text-base-content/60 hover:text-base-content"
        :title="t('a2ui.dataTable.resetFilters')"
      >
        <Icon icon="mynaui:refresh" class="w-3.5 h-3.5" />
      </button>
    </div>

    <!-- Table -->
    <div class="overflow-x-auto rounded-xl border border-base-300">
      <div v-if="isLoading" class="flex justify-center items-center py-16">
        <span class="loading loading-spinner text-primary loading-md"></span>
      </div>
      <table v-else class="table table-xs w-full font-mono">
        <thead
          class="text-base-content/70 bg-base-300/60 uppercase select-none font-sans text-[11px]"
        >
          <tr>
            <th
              v-for="col in columns"
              :key="col.key"
              @click="col.sortable !== false ? toggleSort(col.key) : null"
              class="hover:text-base-content py-2 px-3"
              :class="[
                col.align === 'right'
                  ? 'text-right'
                  : col.align === 'center'
                    ? 'text-center'
                    : 'text-left',
                col.sortable !== false ? 'cursor-pointer' : '',
              ]"
            >
              <div class="inline-flex items-center gap-1">
                <span>{{ col.label }}</span>
                <span v-if="sortKey === col.key" class="text-primary font-bold">
                  {{ sortAsc ? "↑" : "↓" }}
                </span>
              </div>
            </th>
          </tr>
        </thead>
        <tbody class="divide-y divide-base-300/60 text-base-content/90 text-xs">
          <tr
            v-for="(row, idx) in processedRows"
            :key="idx"
            class="hover:bg-base-300/40 transition-all"
          >
            <td
              v-for="col in columns"
              :key="col.key"
              class="py-2 px-3"
              :class="[
                col.align === 'right'
                  ? 'text-right'
                  : col.align === 'center'
                    ? 'text-center'
                    : 'text-left',
              ]"
            >
              <!-- Badge format -->
              <div
                v-if="col.format === 'badge'"
                class="flex flex-wrap gap-1 items-center"
                :class="
                  col.align === 'center'
                    ? 'justify-center'
                    : col.align === 'right'
                      ? 'justify-end'
                      : 'justify-start'
                "
              >
                <span
                  v-for="b in getBadgeList(row, col)"
                  :key="b"
                  class="badge badge-xs font-semibold whitespace-nowrap px-2 py-0.5"
                  :class="resolveBadgeClass(b, col)"
                >
                  {{ b }}
                </span>
              </div>
              <!-- Standard cell -->
              <span v-else>
                {{ formatCell(row, col) }}
              </span>
            </td>
          </tr>
          <tr v-if="processedRows.length === 0">
            <td
              :colspan="columns.length"
              class="text-center py-8 text-base-content/50 font-sans text-xs"
            >
              {{ t("a2ui.dataTable.noMatchingRecords") }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Table Footer / Count -->
    <div class="flex justify-between items-center text-xs text-base-content/60 font-sans pt-1">
      <span>{{ t("a2ui.dataTable.showingRows", { count: processedRows.length }) }}</span>
    </div>
  </div>
</template>
