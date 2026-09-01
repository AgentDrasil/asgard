<script setup lang="ts">
import { ref, computed, watch, onMounted } from "vue";
import { Icon } from "@iconify/vue";
import Papa from "papaparse";
import type { A2UIHoldingsTableWidget, A2UIDataTableColumn } from "../../types/a2ui";
import { resolveBadgeClass } from "../../utils/badgeHelper";
import {
  formatA2UIMoney,
  formatA2UIPercent,
  formatA2UINumber,
  fetchWorkspaceAsset,
} from "../../utils/a2uiUtils";

const props = defineProps<{
  widget: A2UIHoldingsTableWidget;
  sessionId?: string;
  manifestPath?: string | null;
}>();

const rawRows = ref<any[]>([]);
const headers = ref<string[]>([]);
const isLoading = ref<boolean>(false);

const searchQuery = ref<string>("");
const activeFilters = ref<Record<string, string>>({});
const isAggregateMode = ref<boolean>(false);
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
      console.error("Failed to load sourceCsv for A2UIHoldingsTable:", e);
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

// Detect or configure primary numeric/value column
const valueKey = computed(() => {
  if (props.widget.valueField) return props.widget.valueField;
  const currCol = columns.value.find((c) => c.format === "currency");
  if (currCol) return currCol.key;
  const numCol = columns.value.find((c) => c.format === "number" && c.key !== "id");
  if (numCol) return numCol.key;
  const candidate = Object.keys(rawRows.value[0] || {}).find((k) =>
    /value|amount|total|price|balance|cost/i.test(k),
  );
  return candidate || "";
});

// Detect or configure weight/percentage column
const weightKey = computed(() => {
  if (props.widget.weightField) return props.widget.weightField;
  const pctCol = columns.value.find(
    (c) => c.format === "percent" || /pct|weight|percent|share/i.test(c.key),
  );
  if (pctCol) return pctCol.key;
  return "pct_of_portfolio";
});

const totalPortfolioValue = computed(() => {
  const k = valueKey.value;
  if (!k) return 0;
  return rawRows.value.reduce((sum, r) => {
    const v = parseFloat(String(r[k] || 0).replace(/,/g, ""));
    return sum + (isNaN(v) ? 0 : v);
  }, 0);
});

// Processed Rows (Filtered + Aggregated + Sorted)
const processedRows = computed(() => {
  const total = totalPortfolioValue.value;
  const vKey = valueKey.value;
  const wKey = weightKey.value;

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

  // 2. Aggregate if requested
  const aggKey = props.widget.features?.aggregateBy;
  let list = filtered.map((r) => {
    const mval = vKey ? parseFloat(String(r[vKey] || 0).replace(/,/g, "")) : 0;
    const pct = total > 0 && !isNaN(mval) ? (mval / total) * 100 : 0;
    return {
      ...r,
      [wKey]: r[wKey] !== undefined ? r[wKey] : pct,
    };
  });

  if (isAggregateMode.value && aggKey) {
    const groupMap: Record<string, any> = {};
    filtered.forEach((row) => {
      const primary = String(row[aggKey] || "OTHER");
      if (!groupMap[primary]) {
        const item: Record<string, any> = {
          ...row,
          _raw_sets: {} as Record<string, Set<string>>,
          _is_aggregated: true,
        };
        columns.value.forEach((col) => {
          if (col.format === "badge" || typeof row[col.key] === "string") {
            item._raw_sets[col.key] = new Set([String(row[col.key] || "")].filter(Boolean));
          }
        });
        groupMap[primary] = item;
      } else {
        columns.value.forEach((col) => {
          if (col.format === "badge" || typeof row[col.key] === "string") {
            if (!groupMap[primary]._raw_sets[col.key]) {
              groupMap[primary]._raw_sets[col.key] = new Set();
            }
            if (row[col.key]) {
              groupMap[primary]._raw_sets[col.key].add(String(row[col.key]));
            }
          }
        });

        // Sum numeric fields
        for (const col of columns.value) {
          if (
            ["currency", "number", "percent"].includes(col.format || "") &&
            col.key !== aggKey &&
            col.key !== wKey
          ) {
            const numA = parseFloat(String(groupMap[primary][col.key] || 0).replace(/,/g, ""));
            const numB = parseFloat(String(row[col.key] || 0).replace(/,/g, ""));
            if (!isNaN(numA) && !isNaN(numB)) {
              groupMap[primary][col.key] = numA + numB;
            }
          }
        }
      }
    });

    list = Object.values(groupMap).map((g) => {
      const mval = vKey ? parseFloat(String(g[vKey] || 0).replace(/,/g, "")) : 0;
      const pct = total > 0 && !isNaN(mval) ? (mval / total) * 100 : 0;
      const res: Record<string, any> = {
        ...g,
        [wKey]: pct,
      };
      if (g._raw_sets) {
        for (const [k, set] of Object.entries(g._raw_sets as Record<string, Set<string>>)) {
          res[k] = Array.from(set).join(", ");
          res[`${k}_list`] = Array.from(set);
          res[`${k}s_list`] = Array.from(set);
          res[`${k}_count`] = set.size;
        }
      }
      return res;
    });
  }

  // 3. Sort
  if (!sortKey.value) return list;
  return [...list].sort((a, b) => {
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

  if (row[`${col.key}s_list`] && Array.isArray(row[`${col.key}s_list`])) {
    return row[`${col.key}s_list`].map((v: any) => String(v).trim()).filter(Boolean);
  }
  if (row[`${col.key}_list`] && Array.isArray(row[`${col.key}_list`])) {
    return row[`${col.key}_list`].map((v: any) => String(v).trim()).filter(Boolean);
  }

  const strVal = String(val);
  if (strVal.includes(",")) {
    return strVal
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
  }
  return [strVal];
}

function formatBadgeLabel(badgeText: string, col?: any): string {
  if (!badgeText) return "";
  const key = col?.key?.toLowerCase() || "";
  if (key.includes("tax") || badgeText.toLowerCase().startsWith("tax-")) {
    if (badgeText.includes("-")) {
      const parts = badgeText.split("-");
      return parts[parts.length - 1].trim();
    }
  }
  return badgeText;
}

function formatCell(row: any, col: any): string {
  const val = row[col.key];
  if (col.format === "currency") {
    return formatA2UIMoney(val);
  }
  if (col.format === "number") {
    return formatA2UINumber(val);
  }
  if (col.format === "percent") {
    const num = parseFloat(String(val || 0).replace(/,/g, ""));
    if (isNaN(num)) return String(val ?? "");
    if (col.key === "pct_of_portfolio" || col.key === "pct" || col.key.includes("weight")) {
      return `${num.toFixed(2)}%`;
    }
    return formatA2UIPercent(num);
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
        <span>Export CSV</span>
      </button>
    </div>

    <!-- Controls Toolbar -->
    <div
      v-if="
        widget.features?.aggregateBy ||
        widget.features?.search !== false ||
        (widget.features?.filters && widget.features.filters.length > 0)
      "
      class="flex flex-wrap items-center gap-2 pt-1 pb-3 border-b border-base-300"
    >
      <!-- Cross-Account Aggregate Assets Button -->
      <button
        v-if="widget.features?.aggregateBy"
        @click="isAggregateMode = !isAggregateMode"
        class="btn btn-xs sm:btn-sm gap-1.5 text-xs font-semibold rounded-lg transition-all"
        :class="
          isAggregateMode
            ? 'btn-primary shadow-xs'
            : 'btn-outline border-base-300 text-base-content/70 hover:bg-base-300 hover:text-base-content'
        "
        title="Consolidate positions with identical symbols across all accounts"
      >
        <Icon icon="lucide:combine" class="w-3.5 h-3.5" />
        <span>{{ isAggregateMode ? "Aggregated" : "Aggregate Assets" }}</span>
      </button>

      <!-- Search Input -->
      <div v-if="widget.features?.search !== false" class="relative w-full sm:w-48">
        <Icon
          icon="material-symbols:search"
          class="w-4 h-4 absolute left-2.5 top-2 text-base-content/50"
        />
        <input
          v-model="searchQuery"
          type="text"
          :placeholder="columns[0] ? `Search ${columns[0].label}...` : 'Search...'"
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
          <option value="">All {{ f.label || f.key }}</option>
          <option v-for="opt in filterOptionsMap[f.key] || []" :key="opt" :value="opt">
            {{ opt }}
          </option>
        </select>
      </template>

      <!-- Reset Button -->
      <button
        @click="resetFilters"
        class="btn btn-xs sm:btn-sm btn-ghost text-base-content/60 hover:text-base-content"
        title="Reset Filters"
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
              <!-- Multi-account multi-line list for aggregated positions -->
              <div
                v-if="col.key === 'account_label' && row._is_aggregated && row.account_count > 1"
                class="flex flex-col gap-1 font-sans py-1 min-w-[140px]"
              >
                <div class="flex items-center gap-1.5">
                  <span class="badge badge-xs badge-tag-sky font-bold shrink-0 px-2 py-0.5"
                    >{{ row.account_count }} Accounts</span
                  >
                </div>
                <div class="flex flex-col gap-0.5 text-[11px] text-base-content/80 leading-tight">
                  <span
                    v-for="acc in row.accounts_list || row.account_label.split(', ')"
                    :key="acc"
                    class="truncate max-w-[220px]"
                    :title="acc"
                  >
                    • {{ acc }}
                  </span>
                </div>
              </div>
              <!-- Badge format (split multi-badge & multi-line wrap) -->
              <div
                v-else-if="col.format === 'badge'"
                class="flex flex-wrap gap-1 items-center max-w-[240px]"
              >
                <span
                  v-for="b in getBadgeList(row, col)"
                  :key="b"
                  class="badge badge-xs font-semibold whitespace-nowrap px-2 py-0.5"
                  :class="resolveBadgeClass(b, col)"
                  :title="b"
                >
                  {{ formatBadgeLabel(b, col) }}
                </span>
              </div>
              <!-- Standard cell -->
              <span v-else :class="col.key === 'symbol' ? 'font-bold text-base-content' : ''">
                {{ formatCell(row, col) }}
              </span>
            </td>
          </tr>
          <tr v-if="processedRows.length === 0">
            <td
              :colspan="columns.length"
              class="text-center py-8 text-base-content/50 font-sans text-xs"
            >
              No matching records found
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Table Footer / Count -->
    <div class="flex justify-between items-center text-xs text-base-content/60 font-sans pt-1">
      <span
        >Showing {{ processedRows.length }}
        {{ isAggregateMode ? "consolidated items" : "rows" }}</span
      >
    </div>
  </div>
</template>
