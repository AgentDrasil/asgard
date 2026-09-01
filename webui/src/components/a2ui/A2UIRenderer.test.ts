// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import A2UIRenderer from "./A2UIRenderer.vue";
import type { A2UIManifest } from "../../types/a2ui";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock Chart.js to avoid jsdom canvas issues
vi.mock("chart.js", () => {
  function MockChart() {
    return {
      destroy: vi.fn<() => void>(),
    };
  }
  MockChart.register = vi.fn<() => void>();
  return {
    Chart: MockChart,
    registerables: [],
  };
});

describe("A2UIRenderer.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  const sampleManifest: A2UIManifest = {
    schemaVersion: "1.0",
    title: "Test Wealth Dashboard",
    subtitle: "Realtime Analytics",
    asOfDate: "2026-08-31",
    kpis: [
      {
        id: "kpi-1",
        label: "Total Net Worth",
        value: 1500000,
        format: "currency",
        change: "+12.5%",
        changeType: "positive",
      },
      {
        id: "kpi-2",
        label: "Return Rate",
        value: 8.4,
        format: "percent",
      },
    ],
    tabs: [
      {
        id: "tab-overview",
        label: "Overview",
        icon: "layers",
        layout: "grid-2",
        widgets: [
          {
            id: "widget-chart",
            type: "chart",
            title: "Allocation",
            chartType: "donut",
            labels: ["US", "Foreign"],
            datasets: [{ data: [70, 30] }],
          },
          {
            id: "widget-keyval",
            type: "key-val-list",
            title: "Summary Breakdown",
            items: [
              { label: "Equities", value: 1000000, format: "currency", progressPct: 66 },
              { label: "Cash", value: 500000, format: "currency", progressPct: 33 },
            ],
          },
        ],
      },
      {
        id: "tab-positions",
        label: "Positions",
        icon: "table",
        layout: "stacked",
        widgets: [
          {
            id: "widget-table",
            type: "data-table",
            title: "All Positions",
            rows: [
              { symbol: "AAPL", shares: 100, price: 230 },
              { symbol: "GOOGL", shares: 50, price: 180 },
            ],
          },
        ],
      },
    ],
  };

  it("renders header title, subtitle, asOfDate and KPI cards", async () => {
    const app = createApp({
      render() {
        return h(A2UIRenderer, {
          manifest: sampleManifest,
          sessionId: "sess-1",
        });
      },
    });
    app.mount(root);

    await nextTick();

    expect(root.textContent).toContain("Test Wealth Dashboard");
    expect(root.textContent).toContain("Realtime Analytics");
    expect(root.textContent).toContain("As of: 2026-08-31");
    expect(root.textContent).toContain("Total Net Worth");
    expect(root.textContent).toContain("$1,500,000.00");
    expect(root.textContent).toContain("+12.5%");
    expect(root.textContent).toContain("Return Rate");
    expect(root.textContent).toContain("+8.40%");

    app.unmount();
  });

  it("renders tab buttons and allows switching tabs", async () => {
    const app = createApp({
      render() {
        return h(A2UIRenderer, {
          manifest: sampleManifest,
          sessionId: "sess-1",
        });
      },
    });
    app.mount(root);

    await nextTick();

    const buttons = Array.from(root.querySelectorAll("button"));
    const overviewBtn = buttons.find((b) => b.textContent?.includes("Overview"));
    const positionsBtn = buttons.find((b) => b.textContent?.includes("Positions"));

    expect(overviewBtn).toBeDefined();
    expect(positionsBtn).toBeDefined();

    // Initially Overview widgets are rendered
    expect(root.textContent).toContain("Summary Breakdown");
    expect(root.textContent).toContain("Equities");

    // Click Positions tab
    positionsBtn?.click();
    await nextTick();

    expect(root.textContent).toContain("All Positions");
    expect(root.textContent).toContain("AAPL");
    expect(root.textContent).toContain("GOOGL");

    app.unmount();
  });
});
