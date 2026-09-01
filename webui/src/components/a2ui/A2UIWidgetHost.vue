<script setup lang="ts">
import type { A2UIWidget } from "../../types/a2ui";
import A2UIChart from "./A2UIChart.vue";
import A2UIDataTable from "./A2UIDataTable.vue";
import A2UIHoldingsTable from "./A2UIHoldingsTable.vue";
import A2UIMarkdown from "./A2UIMarkdown.vue";
import A2UIKeyValList from "./A2UIKeyValList.vue";

defineProps<{
  widget: A2UIWidget;
  sessionId?: string;
  manifestPath?: string | null;
}>();
</script>

<template>
  <A2UIChart v-if="widget.type === 'chart'" :widget="widget" />
  <A2UIHoldingsTable
    v-else-if="widget.type === 'holdings-table'"
    :widget="widget"
    :sessionId="sessionId"
    :manifestPath="manifestPath"
  />
  <A2UIDataTable
    v-else-if="widget.type === 'data-table'"
    :widget="widget"
    :sessionId="sessionId"
    :manifestPath="manifestPath"
  />
  <A2UIMarkdown
    v-else-if="widget.type === 'markdown'"
    :widget="widget"
    :sessionId="sessionId"
    :manifestPath="manifestPath"
  />
  <A2UIKeyValList v-else-if="widget.type === 'key-val-list'" :widget="widget" />
</template>
