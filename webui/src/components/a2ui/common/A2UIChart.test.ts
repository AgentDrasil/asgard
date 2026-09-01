// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import A2UIChart from "./A2UIChart.vue";
import type { A2UIChartWidget } from "../../../types/a2ui";

const mockChartInstances: any[] = [];
const mockChartConstructor = vi.fn<(...args: any[]) => void>();

vi.mock("chart.js", () => {
  function MockChart(this: any, canvas: any, config: any) {
    mockChartConstructor(canvas, config);
    this.canvas = canvas;
    this.config = config;
    this.options = config.options;
    this.data = config.data;
    this.destroy = vi.fn<() => void>();
    this.update = vi.fn<(mode?: string) => void>();
    mockChartInstances.push(this);
    return this;
  }
  MockChart.register = vi.fn<(...args: any[]) => void>();
  return {
    Chart: MockChart,
    registerables: [],
  };
});

describe("A2UIChart.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    mockChartConstructor.mockClear();
    mockChartInstances.length = 0;
    document.documentElement.removeAttribute("data-theme");
  });

  const sampleDonutWidget: A2UIChartWidget = {
    id: "chart-asset-alloc",
    type: "chart",
    title: "Strategic Asset Class Allocation",
    chartType: "doughnut",
    labels: ["US Equities", "International Equities", "Cash & Equivalents"],
    datasets: [
      {
        data: [60, 25, 15],
      },
    ],
  };

  it("initializes Chart with dark theme colors by default", async () => {
    document.documentElement.setAttribute("data-theme", "dark");

    const app = createApp({
      render() {
        return h(A2UIChart, {
          widget: sampleDonutWidget,
        });
      },
    });
    app.mount(root);
    await nextTick();

    expect(mockChartConstructor).toHaveBeenCalled();
    const config = mockChartConstructor.mock.calls[0][1];

    const legendColor = config.options?.plugins?.legend?.labels?.color;
    expect(legendColor).not.toBe("currentColor");
    expect(legendColor).toBe("#f1f5f9");

    app.unmount();
  });

  it("adapts colors to light theme on mount", async () => {
    document.documentElement.setAttribute("data-theme", "light");

    const app = createApp({
      render() {
        return h(A2UIChart, {
          widget: sampleDonutWidget,
        });
      },
    });
    app.mount(root);
    await nextTick();

    expect(mockChartConstructor).toHaveBeenCalled();
    const config = mockChartConstructor.mock.calls[0][1];

    const legendColor = config.options?.plugins?.legend?.labels?.color;
    expect(legendColor).toBe("#1e293b");

    app.unmount();
  });

  it("deep merges user custom options and scales without losing theme colors", async () => {
    document.documentElement.setAttribute("data-theme", "dark");

    const barWidgetWithOptions: A2UIChartWidget = {
      id: "chart-bar-custom",
      type: "chart",
      title: "Monthly Revenue",
      chartType: "bar",
      labels: ["Q1", "Q2"],
      datasets: [{ data: [100, 200] }],
      options: {
        scales: {
          y: {
            min: 0,
            max: 500,
          },
        },
      },
    };

    const app = createApp({
      render() {
        return h(A2UIChart, {
          widget: barWidgetWithOptions,
        });
      },
    });
    app.mount(root);
    await nextTick();

    expect(mockChartConstructor).toHaveBeenCalled();
    const config = mockChartConstructor.mock.calls[0][1];
    expect(config.options?.scales?.y?.min).toBe(0);
    expect(config.options?.scales?.y?.max).toBe(500);
    expect(config.options?.scales?.y?.ticks?.color).toBe("rgba(226, 232, 240, 0.75)");
    expect(config.options?.scales?.x?.ticks?.color).toBe("rgba(226, 232, 240, 0.75)");

    app.unmount();
  });

  it("updates chart instance in-place when theme changes via MutationObserver", async () => {
    document.documentElement.setAttribute("data-theme", "dark");

    const app = createApp({
      render() {
        return h(A2UIChart, {
          widget: sampleDonutWidget,
        });
      },
    });
    app.mount(root);
    await nextTick();

    expect(mockChartInstances.length).toBe(1);
    const instance = mockChartInstances[0];
    expect(instance.options.plugins.legend.labels.color).toBe("#f1f5f9");

    // Trigger theme mutation to light
    document.documentElement.setAttribute("data-theme", "light");
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await nextTick();

    // Verify chart.update was called in-place with "none" and no new Chart instance was created
    expect(instance.update).toHaveBeenCalledWith("none");
    expect(mockChartInstances.length).toBe(1);
    expect(instance.options.plugins.legend.labels.color).toBe("#1e293b");

    app.unmount();
  });
});
