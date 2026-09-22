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
  compressed_outputs: 8,
  truncations: 3,
  show_output_calls: 2,
  show_output_bytes: 12_697,
  jev_avg_tokens: 125,
  compass_avg_tokens: 200,
  show_output_rate: 0.25,
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
    expect(root.textContent).toContain("Compression Quality");
    expect(root.textContent).toContain("Avg Tokens / Call");
    expect(root.textContent).toContain("LLM Raw Retrievals (show-output)");
    expect(root.textContent).toContain("Retrieval Rate");
    expect(root.textContent).toContain("Compressed Outputs");
    expect(root.textContent).toContain("Raw Output Volume");

    app.unmount();
  });

  it("presents raw retrievals as a rate against compressed outputs", async () => {
    const app = await mount();

    // 2 retrievals over 8 compressed outputs.
    expect(root.textContent).toContain("25.0%");

    app.unmount();
  });

  it("avoids reporting a reassuring rate when nothing was compressed", async () => {
    vi.spyOn(api, "getCompressionMetrics").mockResolvedValue({
      ...sampleMetrics,
      compressed_outputs: 0,
      show_output_rate: 0,
    });
    const app = await mount();

    // A 0% reading would imply the agent never rejected a result, which is not
    // a claim we can make with an empty denominator.
    expect(root.textContent).not.toContain("0.0%");
    expect(root.textContent).toContain("—");

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
    expect(root.textContent).toContain("压缩质量");
    expect(root.textContent).toContain("LLM 取回原文次数（show-output）");
    expect(root.textContent).toContain("取回率");
    expect(root.textContent).toContain("被压缩的命令数");

    app.unmount();
  });

  it("shows the empty state when no statistics are available", async () => {
    vi.spyOn(api, "getCompressionMetrics").mockResolvedValue(null);
    const app = await mount();

    expect(root.textContent).toContain("No statistics yet");

    app.unmount();
  });
});
