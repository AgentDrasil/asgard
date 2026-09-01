<script setup lang="ts">
import { ref, onMounted, watch, onBeforeUnmount, nextTick } from "vue";
import { Chart, registerables } from "chart.js";
import type { A2UIChartWidget } from "../../types/a2ui";
import { formatA2UIMoney } from "../../utils/a2uiUtils";

if (typeof Chart?.register === "function") {
  Chart.register(...registerables);
}

const props = defineProps<{
  widget: A2UIChartWidget;
}>();

const canvasRef = ref<HTMLCanvasElement | null>(null);
let chartInstance: Chart | null = null;

function renderChart() {
  if (!canvasRef.value) return;
  if (chartInstance) {
    chartInstance.destroy();
    chartInstance = null;
  }

  const rawType = props.widget.chartType || "doughnut";
  const isHorizontal = rawType === "horizontal-bar";
  // Chart.js uses 'doughnut', normalize 'donut' to 'doughnut'
  const chartType = isHorizontal ? "bar" : rawType === "donut" ? "doughnut" : rawType;
  const isDonutOrPie = chartType === "doughnut" || chartType === "pie";

  const defaultColors = [
    "#22c55e",
    "#0ea5e9",
    "#f59e0b",
    "#8b5cf6",
    "#ec4899",
    "#14b8a6",
    "#f97316",
    "#64748b",
  ];

  const datasets = (props.widget.datasets || []).map((ds) => ({
    ...ds,
    backgroundColor: ds.backgroundColor || defaultColors,
    borderColor:
      ds.borderColor ||
      (isDonutOrPie ? "transparent" : chartType === "line" ? "#3b82f6" : "transparent"),
    borderWidth:
      ds.borderWidth !== undefined
        ? ds.borderWidth
        : isDonutOrPie
          ? 2
          : chartType === "line"
            ? 2
            : 0,
    borderRadius: ds.borderRadius ?? (isDonutOrPie ? 0 : 6),
    pointRadius: ds.pointRadius ?? (chartType === "line" ? 2 : undefined),
    pointHoverRadius: ds.pointHoverRadius ?? (chartType === "line" ? 5 : undefined),
  }));

  const configOptions: any = {
    responsive: true,
    maintainAspectRatio: false,
    indexAxis: isHorizontal ? "y" : "x",
    plugins: {
      legend: {
        display:
          props.widget.options?.plugins?.legend?.display ?? (isDonutOrPie || chartType === "line"),
        position: isDonutOrPie ? "right" : "top",
        labels: {
          color: "currentColor",
          font: { family: "Inter, system-ui, sans-serif", size: 11 },
          boxWidth: 12,
          boxHeight: 12,
          padding: 10,
        },
      },
      tooltip: {
        backgroundColor: "rgba(15, 23, 42, 0.9)",
        titleColor: "#f8fafc",
        bodyColor: "#cbd5e1",
        borderColor: "rgba(100, 116, 139, 0.3)",
        borderWidth: 1,
        padding: 10,
        boxPadding: 4,
        usePointStyle: true,
        callbacks: {
          label: (context: any) => {
            const label = context.dataset.label || context.label || "";
            const val =
              context.parsed?.y !== undefined && !isHorizontal
                ? context.parsed.y
                : context.parsed?.x !== undefined && isHorizontal
                  ? context.parsed.x
                  : context.raw;
            const isCurrency =
              props.widget.format === "currency" ||
              props.widget.yAxisFormat === "currency" ||
              context.dataset.format === "currency";
            const isPercent =
              props.widget.format === "percent" ||
              props.widget.yAxisFormat === "percent" ||
              context.dataset.format === "percent";
            if (typeof val === "number") {
              if (isCurrency) {
                return ` ${label}: ${formatA2UIMoney(val)}`;
              }
              if (isPercent) {
                return ` ${label}: ${val.toFixed(2)}%`;
              }
              return ` ${label}: ${val.toLocaleString()}`;
            }
            return ` ${label}: ${val}`;
          },
        },
      },
    },
  };

  if (!isDonutOrPie) {
    const isCurrency =
      props.widget.yAxisFormat === "currency" || props.widget.format === "currency";
    const isPercent = props.widget.yAxisFormat === "percent" || props.widget.format === "percent";
    const prefix = isCurrency ? "$" : "";
    const suffix = isPercent ? "%" : "";

    configOptions.scales = {
      x: {
        grid: { color: "rgba(100, 116, 139, 0.15)" },
        ticks: { color: "rgba(148, 163, 184, 0.9)", font: { size: 10 } },
      },
      y: {
        grid: { color: "rgba(100, 116, 139, 0.15)" },
        ticks: {
          color: "rgba(148, 163, 184, 0.9)",
          font: { size: 10 },
          callback: (value: any) => {
            if (typeof value === "number") {
              if (value >= 1_000_000_000)
                return `${prefix}${(value / 1_000_000_000).toFixed(1)}B${suffix}`;
              if (value >= 1_000_000) return `${prefix}${(value / 1_000_000).toFixed(1)}M${suffix}`;
              if (value >= 10_000) return `${prefix}${(value / 1_000).toFixed(0)}k${suffix}`;
              return `${prefix}${value}${suffix}`;
            }
            return value;
          },
        },
      },
    };
  } else {
    configOptions.cutout = "65%";
  }

  try {
    chartInstance = new Chart(canvasRef.value, {
      type: chartType as any,
      data: {
        labels: props.widget.labels || [],
        datasets,
      },
      options: {
        ...configOptions,
        ...props.widget.options,
      },
    });
  } catch (err) {
    console.error("Failed to create Chart.js instance:", err);
  }
}

onMounted(() => {
  nextTick(() => {
    renderChart();
  });
});

watch(
  () => props.widget,
  () => {
    nextTick(() => {
      renderChart();
    });
  },
  { deep: true },
);

onBeforeUnmount(() => {
  if (chartInstance) {
    chartInstance.destroy();
    chartInstance = null;
  }
});
</script>

<template>
  <div
    class="card bg-base-200/80 border border-base-300 p-5 sm:p-6 shadow-xs flex flex-col min-h-[340px] rounded-xl"
  >
    <div v-if="widget.title" class="pb-3 mb-4 border-b border-base-300">
      <h3 class="text-sm sm:text-base font-bold text-base-content">{{ widget.title }}</h3>
      <p v-if="widget.description" class="text-xs text-base-content/60 mt-0.5">
        {{ widget.description }}
      </p>
    </div>

    <div class="relative w-full flex-1 min-h-[240px]">
      <canvas ref="canvasRef"></canvas>
    </div>
  </div>
</template>
