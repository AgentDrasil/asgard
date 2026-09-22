// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import CompressionMetricsView from "./CompressionMetricsView.vue";
import * as api from "../lib/api";
import { i18n, setLocale } from "../i18n";
import type { CompressionMetrics } from "../types";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock vue-router
const mockPush = vi.fn<() => void>();
const mockBack = vi.fn<() => void>();
vi.mock("vue-router", () => ({
  useRouter: () => ({
    push: mockPush,
    back: mockBack,
  }),
}));

const sampleMetrics: CompressionMetrics = {
  jev_calls: 12,
  jev_tokens: 1500,
  compass_calls: 4,
  compass_tokens: 800,
  truncations: 3,
  show_output_calls: 7,
  show_output_bytes: 12_697,
  jev_avg_tokens: 125,
  compass_avg_tokens: 200,
};

describe("CompressionMetricsView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");

    vi.spyOn(api, "getCompressionMetrics").mockResolvedValue(sampleMetrics);
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
    setLocale("en");
  });

  const flush = async () => {
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();
  };

  const mount = async () => {
    const app = createApp({
      render() {
        return h(CompressionMetricsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();
    return app;
  };

  it("renders grouped statistics in en", async () => {
    const app = await mount();

    expect(root.textContent).toContain("Command Output Compression");
    expect(root.textContent).toContain("Jev (Level 1 Classifier)");
    expect(root.textContent).toContain("Compass (Level 2 Summarizer)");
    expect(root.textContent).toContain("Output Handling");
    expect(root.textContent).toContain("Avg Tokens / Call");
    expect(root.textContent).toContain("show-output Calls");
    expect(root.textContent).toContain("Raw Bytes Retrieved");

    app.unmount();
  });

  it("renders token totals and per-call averages", async () => {
    const app = await mount();

    // count, token total (base-1024 humanized), and average for each model
    expect(root.textContent).toContain("12");
    expect(root.textContent).toContain("1.5K");
    expect(root.textContent).toContain("125.0");
    expect(root.textContent).toContain("200.0");

    app.unmount();
  });

  it("renders raw bytes retrieved in human-readable units", async () => {
    const app = await mount();

    expect(root.textContent).toContain("12.4 KB");

    app.unmount();
  });

  it("renders in zh-CN when locale is switched", async () => {
    setLocale("zh-CN");
    const app = await mount();

    expect(root.textContent).toContain("命令输出压缩统计");
    expect(root.textContent).toContain("Jev（Level 1 分类器）");
    expect(root.textContent).toContain("Compass（Level 2 摘要器）");
    expect(root.textContent).toContain("调用次数");
    expect(root.textContent).toContain("平均每次 Token");
    expect(root.textContent).toContain("show-output 调用次数");

    app.unmount();
  });

  it("shows the empty state when no statistics are available", async () => {
    vi.spyOn(api, "getCompressionMetrics").mockResolvedValue(null);
    const app = await mount();

    expect(root.textContent).toContain("No statistics yet");

    app.unmount();
  });
});
