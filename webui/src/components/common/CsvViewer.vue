<script setup lang="ts">
import { ref, computed } from "vue";
import Papa from "papaparse";
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import { compareValues, type SortDirection } from "../../utils/tableSort";

const { t } = useI18n();

const props = withDefaults(
  defineProps<{
    content: string;
    fileName?: string;
  }>(),
  {
    fileName: "",
  },
);

const sortColumnIndex = ref<number | null>(null);
const sortDirection = ref<SortDirection>(null);
const filterQuery = ref("");

interface ParsedData {
  headers: string[];
  rows: string[][];
  errors: Papa.ParseError[];
}

const parsedCsv = computed<ParsedData>(() => {
  if (!props.content) {
    return { headers: [], rows: [], errors: [] };
  }

  const result = Papa.parse<string[]>(props.content, {
    skipEmptyLines: "greedy",
  });

  const rawRows = result.data || [];
  if (rawRows.length === 0) {
    return { headers: [], rows: [], errors: result.errors };
  }

  // Treat row 0 as header
  const headers = rawRows[0].map((h, i) => (h && h.trim() ? h.trim() : `Column ${i + 1}`));
  const rows = rawRows.slice(1);

  return {
    headers,
    rows,
    errors: result.errors,
  };
});

// Filter rows based on search input
const filteredRows = computed(() => {
  const q = filterQuery.value.trim().toLowerCase();
  if (!q) return parsedCsv.value.rows;

  return parsedCsv.value.rows.filter((row) => row.some((cell) => cell.toLowerCase().includes(q)));
});

// Sorted rows based on header clicks
const displayRows = computed(() => {
  const rows = [...filteredRows.value];
  if (sortColumnIndex.value === null || !sortDirection.value) {
    return rows;
  }

  const colIdx = sortColumnIndex.value;
  const dir = sortDirection.value;

  return rows.sort((rowA, rowB) => {
    const cellA = rowA[colIdx] ?? "";
    const cellB = rowB[colIdx] ?? "";
    return compareValues(cellA, cellB, dir);
  });
});

function handleSort(colIdx: number) {
  if (sortColumnIndex.value === colIdx) {
    if (sortDirection.value === "asc") {
      sortDirection.value = "desc";
    } else if (sortDirection.value === "desc") {
      sortDirection.value = null;
      sortColumnIndex.value = null;
    } else {
      sortDirection.value = "asc";
    }
  } else {
    sortColumnIndex.value = colIdx;
    sortDirection.value = "asc";
  }
}
</script>

<template>
  <div
    class="csv-table-viewer w-full h-full flex flex-col bg-base-100 min-w-0 overflow-hidden text-xs"
  >
    <!-- Top toolbar for table quick search & info -->
    <div
      class="px-3 py-2 bg-base-200/70 border-b border-base-300 flex items-center justify-between gap-3 shrink-0"
    >
      <div class="flex items-center gap-2 text-base-content/70">
        <Icon icon="vscode-icons:file-type-excel" class="w-4 h-4 shrink-0" />
        <span class="font-mono font-medium text-base-content">{{
          t("viewers.csv.colsAndRows", {
            cols: parsedCsv.headers.length,
            rows: parsedCsv.rows.length,
          })
        }}</span>
        <span v-if="filterQuery" class="text-base-content/50">{{
          t("viewers.csv.shown", { count: displayRows.length })
        }}</span>
      </div>

      <div class="flex items-center gap-2">
        <div class="relative flex items-center">
          <Icon
            icon="material-symbols:search"
            class="w-3.5 h-3.5 absolute left-2 text-base-content/50 pointer-events-none"
          />
          <input
            v-model="filterQuery"
            type="text"
            :placeholder="t('viewers.csv.filterPlaceholder')"
            class="input input-xs input-bordered pl-7 pr-6 font-mono text-xs w-36 sm:w-48 bg-base-100 focus:outline-none focus:border-primary"
          />
          <button
            v-if="filterQuery"
            @click="filterQuery = ''"
            class="absolute right-1.5 text-base-content/40 hover:text-base-content"
          >
            <Icon icon="mynaui:x" class="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    </div>

    <!-- Table scroll container -->
    <div class="flex-1 overflow-auto min-h-0 relative">
      <table
        v-if="parsedCsv.headers.length > 0"
        class="table table-xs table-pin-rows w-full border-collapse font-mono select-text"
      >
        <thead>
          <tr class="bg-base-200 text-base-content border-b border-base-300">
            <th
              class="w-10 text-center select-none text-base-content/40 bg-base-200 border-r border-base-300 font-mono"
            >
              #
            </th>
            <th
              v-for="(header, idx) in parsedCsv.headers"
              :key="idx"
              @click="handleSort(idx)"
              class="cursor-pointer select-none border-r border-base-300 hover:bg-base-300/80 transition-colors py-2 px-3 whitespace-nowrap text-left font-semibold group"
              :title="t('viewers.csv.clickToSort', { header })"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="truncate">{{ header }}</span>
                <span
                  class="inline-flex items-center shrink-0 transition-opacity"
                  :class="
                    sortColumnIndex === idx && sortDirection !== null
                      ? 'text-primary opacity-100'
                      : 'opacity-40 group-hover:opacity-80'
                  "
                >
                  <Icon
                    v-if="sortColumnIndex === idx && sortDirection === 'asc'"
                    icon="lucide:chevron-up"
                    class="w-4 h-4 text-primary"
                  />
                  <Icon
                    v-else-if="sortColumnIndex === idx && sortDirection === 'desc'"
                    icon="lucide:chevron-down"
                    class="w-4 h-4 text-primary"
                  />
                  <Icon
                    v-else
                    icon="lucide:chevrons-up-down"
                    class="w-3.5 h-3.5 text-base-content/60"
                  />
                </span>
              </div>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, rIdx) in displayRows"
            :key="rIdx"
            class="hover:bg-base-200/50 border-b border-base-300/50 transition-colors"
          >
            <td
              class="text-center select-none text-base-content/40 border-r border-base-300 bg-base-200/30 font-mono w-10 py-1.5 px-2"
            >
              {{ rIdx + 1 }}
            </td>
            <td
              v-for="(cell, cIdx) in row"
              :key="cIdx"
              class="border-r border-base-300/60 py-1.5 px-3 whitespace-nowrap text-base-content max-w-xs truncate"
              :title="cell"
            >
              {{ cell }}
            </td>
          </tr>
          <tr v-if="displayRows.length === 0">
            <td
              :colspan="parsedCsv.headers.length + 1"
              class="text-center py-8 text-base-content/50"
            >
              {{ t("viewers.csv.noMatchingRecords") }}
            </td>
          </tr>
        </tbody>
      </table>

      <div
        v-else
        class="flex flex-col items-center justify-center h-full text-base-content/50 p-6 text-center"
      >
        <Icon icon="octicon:table-24" class="w-10 h-10 mb-2 opacity-30" />
        <p>{{ t("viewers.csv.noData") }}</p>
      </div>
    </div>
  </div>
</template>
