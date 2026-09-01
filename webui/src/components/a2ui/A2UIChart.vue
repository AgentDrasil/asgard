<script setup lang="ts">
import { ref, onMounted, watch, onBeforeUnmount, nextTick } from "vue";
import { Chart, registerables } from "chart.js";
import type { A2UIChartWidget } from "../../types/a2ui";
import { formatA2UIMoney } from "../../utils/a2uiUtils";
import { isDarkTheme } from "../../utils/themeUtils";

if (typeof Chart?.register === "function") {
  Chart.register(...registerables);
}

const props = defineProps<{
  widget: A2UIChartWidget;
}>();

const canvasRef = ref<HTMLCanvasElement | null>(null);
let chartInstance: Chart | null = null;
let themeObserver: MutationObserver | null = null;
let themeUpdatePending = false;

interface ThemeColors {
  dark: boolean;
  textColor: string;
  mutedColor: string;
  gridColor: string;
  tooltipBg: string;
  tooltipTitle: string;
  tooltipBody: string;
  tooltipBorder: string;
}

function getThemeColors(): ThemeColors {
  const dark = isDarkTheme();
  let textColor = dark ? "#f1f5f9" : "#1e293b";
  let mutedColor = dark ? "rgba(226, 232, 240, 0.75)" : "rgba(30, 41, 59, 0.75)";
  let gridColor = dark ? "rgba(255, 255, 255, 0.08)" : "rgba(0, 0, 0, 0.08)";

  if (typeof window !== "undefined" && canvasRef.value) {
    try {
      const computed = window.getComputedStyle(canvasRef.value);
      if (
        computed.color &&
        computed.color.startsWith("rgb") &&
        computed.color !== "rgba(0, 0, 0, 0)"
      ) {
        textColor = computed.color;
      }
    } catch {
      // Ignore in test or non-standard DOM environments
    }
  }

  return {
    dark,
    textColor,
    mutedColor,
    gridColor,
    tooltipBg: dark ? "rgba(15, 23, 42, 0.95)" : "rgba(255, 255, 255, 0.95)",
    tooltipTitle: dark ? "#f8fafc" : "#0f172a",
    tooltipBody: dark ? "#cbd5e1" : "#334155",
    tooltipBorder: dark ? "rgba(100, 116, 139, 0.3)" : "rgba(203, 213, 225, 0.8)",
  };
}

function buildChartOptions(themeColors: ThemeColors) {
  const rawType = props.widget.chartType || "doughnut";
  const isHorizontal = rawType === "horizontal-bar";
  // Chart.js uses 'doughnut', normalize 'donut' to 'doughnut'
  const chartType = isHorizontal ? "bar" : rawType === "donut" ? "doughnut" : rawType;
  const isDonutOrPie = chartType === "doughnut" || chartType === "pie";

  const baseOptions: any = {
    responsive: true,
    maintainAspectRatio: false,
    indexAxis: isHorizontal ? "y" : "x",
    plugins: {
      legend: {
        display:
          props.widget.options?.plugins?.legend?.display ?? (isDonutOrPie || chartType === "line"),
        position: isDonutOrPie ? "right" : "top",
        labels: {
          color: themeColors.textColor,
          font: { family: "Inter, system-ui, sans-serif", size: 11 },
          boxWidth: 12,
          boxHeight: 12,
          padding: 10,
        },
      },
      tooltip: {
        backgroundColor: themeColors.tooltipBg,
        titleColor: themeColors.tooltipTitle,
        bodyColor: themeColors.tooltipBody,
        borderColor: themeColors.tooltipBorder,
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

    baseOptions.scales = {
      x: {
        grid: { color: themeColors.gridColor },
        ticks: { color: themeColors.mutedColor, font: { size: 10 } },
      },
      y: {
        grid: { color: themeColors.gridColor },
        ticks: {
          color: themeColors.mutedColor,
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
    baseOptions.cutout = "65%";
  }

  const userOptions = props.widget.options;
  if (!userOptions) return baseOptions;

  const merged = { ...baseOptions, ...userOptions };

  merged.plugins = {
    ...baseOptions.plugins,
    ...userOptions.plugins,
    legend: {
      ...baseOptions.plugins?.legend,
      ...userOptions.plugins?.legend,
      labels: {
        ...baseOptions.plugins?.legend?.labels,
        ...userOptions.plugins?.legend?.labels,
      },
    },
    tooltip: {
      ...baseOptions.plugins?.tooltip,
      ...userOptions.plugins?.tooltip,
    },
  };

  if (baseOptions.scales || userOptions.scales) {
    merged.scales = {
      ...baseOptions.scales,
      ...userOptions.scales,
      x: {
        ...baseOptions.scales?.x,
        ...userOptions.scales?.x,
        grid: {
          ...baseOptions.scales?.x?.grid,
          ...userOptions.scales?.x?.grid,
        },
        ticks: {
          ...baseOptions.scales?.x?.ticks,
          ...userOptions.scales?.x?.ticks,
        },
      },
      y: {
        ...baseOptions.scales?.y,
        ...userOptions.scales?.y,
        grid: {
          ...baseOptions.scales?.y?.grid,
          ...userOptions.scales?.y?.grid,
        },
        ticks: {
          ...baseOptions.scales?.y?.ticks,
          ...userOptions.scales?.y?.ticks,
        },
      },
    };
  }

  return merged;
}

function getDatasets(chartType: string, isDonutOrPie: boolean) {
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

  return (props.widget.datasets || []).map((ds) => ({
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
}

function renderChart() {
  if (!canvasRef.value) return;
  if (chartInstance) {
    chartInstance.destroy();
    chartInstance = null;
  }

  const rawType = props.widget.chartType || "doughnut";
  const isHorizontal = rawType === "horizontal-bar";
  const chartType = isHorizontal ? "bar" : rawType === "donut" ? "doughnut" : rawType;
  const isDonutOrPie = chartType === "doughnut" || chartType === "pie";

  const themeColors = getThemeColors();
  const options = buildChartOptions(themeColors);
  const datasets = getDatasets(chartType, isDonutOrPie);

  try {
    chartInstance = new Chart(canvasRef.value, {
      type: chartType as any,
      data: {
        labels: props.widget.labels || [],
        datasets,
      },
      options,
    });
  } catch (err) {
    console.error("Failed to create Chart.js instance:", err);
  }
}

function updateThemeInChart() {
  if (!chartInstance) {
    renderChart();
    return;
  }
  const themeColors = getThemeColors();
  chartInstance.options = buildChartOptions(themeColors);
  chartInstance.update("none");
}

function onThemeChange() {
  if (themeUpdatePending) return;
  themeUpdatePending = true;
  if (typeof requestAnimationFrame !== "undefined") {
    requestAnimationFrame(() => {
      themeUpdatePending = false;
      updateThemeInChart();
    });
  } else {
    setTimeout(() => {
      themeUpdatePending = false;
      updateThemeInChart();
    }, 0);
  }
}

onMounted(() => {
  renderChart();

  if (typeof document !== "undefined") {
    themeObserver = new MutationObserver(() => {
      onThemeChange();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
  }
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
  if (themeObserver) {
    themeObserver.disconnect();
    themeObserver = null;
  }
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
