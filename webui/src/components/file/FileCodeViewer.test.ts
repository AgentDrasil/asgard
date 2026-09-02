// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import FileCodeViewer from "./FileCodeViewer.vue";
import * as api from "../../lib/api";
import { i18n } from "../../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock useShiki
vi.mock("../../composables/useShiki", () => ({
  useShiki: () => ({
    highlightToHtml: (_content: string, _lang: string) => "",
  }),
}));

// Mock Chart.js
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

describe("FileCodeViewer.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders MediaViewer for image files and hides line gutter", async () => {
    vi.spyOn(api, "getFileContent").mockResolvedValueOnce({
      path: "/src/assets/logo.png",
      name: "logo.png",
      ext: "png",
      size: 4096,
      content: "",
      isBinary: true,
      updatedAt: "2026-08-31T10:00:00Z",
    });

    const app = createApp({
      render() {
        return h(FileCodeViewer, {
          sessionId: "sess-abc",
          filePath: "/src/assets/logo.png",
          comments: new Map(),
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector(".media-viewer")).not.toBeNull();
    });

    const img = root.querySelector("img");
    expect(img).not.toBeNull();
    expect(img?.getAttribute("src")).toBe(
      "/api/files/content?session_id=sess-abc&path=%2Fsrc%2Fassets%2Flogo.png&raw=1",
    );
    // Table/line gutter and Find button should NOT be rendered
    expect(root.querySelector("table")).toBeNull();
    expect(root.querySelector('button[title^="Find in file"]')).toBeNull();

    app.unmount();
  });

  it("renders CsvViewer for CSV files and supports table preview and sorting", async () => {
    vi.spyOn(api, "getFileContent").mockResolvedValueOnce({
      path: "/data/users.csv",
      name: "users.csv",
      ext: "csv",
      size: 100,
      content: "Name,Age\nAlice,30\nBob,25",
      isBinary: false,
      updatedAt: "2026-08-31T10:00:00Z",
    });

    const app = createApp({
      render() {
        return h(FileCodeViewer, {
          sessionId: "sess-abc",
          filePath: "/data/users.csv",
          comments: new Map(),
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector(".csv-table-viewer")).not.toBeNull();
    });

    expect(root.textContent).toContain("2 cols × 2 rows");
    expect(root.textContent).toContain("Alice");
    expect(root.textContent).toContain("Bob");

    app.unmount();
  });

  it("renders MediaViewer for SVG files prioritised over code viewer", async () => {
    vi.spyOn(api, "getFileContent").mockResolvedValueOnce({
      path: "/src/assets/icon.svg",
      name: "icon.svg",
      ext: "svg",
      size: 512,
      content: "<svg></svg>",
      isBinary: false,
      updatedAt: "2026-08-31T10:00:00Z",
    });

    const app = createApp({
      render() {
        return h(FileCodeViewer, {
          sessionId: "sess-abc",
          filePath: "/src/assets/icon.svg",
          comments: new Map(),
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector(".media-viewer")).not.toBeNull();
    });

    expect(root.querySelector("table")).toBeNull();

    app.unmount();
  });

  it("renders binary fallback card when file is non-media binary", async () => {
    vi.spyOn(api, "getFileContent").mockResolvedValueOnce({
      path: "/build/app.wasm",
      name: "app.wasm",
      ext: "wasm",
      size: 65536,
      content: "",
      isBinary: true,
      updatedAt: "2026-08-31T10:00:00Z",
    });

    const app = createApp({
      render() {
        return h(FileCodeViewer, {
          sessionId: "sess-abc",
          filePath: "/build/app.wasm",
          comments: new Map(),
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector(".media-viewer")).not.toBeNull();
    });

    expect(root.textContent).toContain("Binary file");
    expect(root.textContent).not.toContain("Download File");
    expect(root.querySelector("a")).toBeNull();
    expect(root.querySelector("table")).toBeNull();

    app.unmount();
  });

  it("renders code line numbers, find button, and supports line comments for code files", async () => {
    vi.spyOn(api, "getFileContent").mockResolvedValueOnce({
      path: "/src/main.ts",
      name: "main.ts",
      ext: "ts",
      size: 50,
      content: "console.log('hello');\nconst a = 1;",
      isBinary: false,
      updatedAt: "2026-08-31T10:00:00Z",
    });

    const app = createApp({
      render() {
        return h(FileCodeViewer, {
          sessionId: "sess-abc",
          filePath: "/src/main.ts",
          comments: new Map(),
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.textContent).toContain("console.log('hello');");
    });

    expect(root.querySelector(".media-viewer")).toBeNull();
    expect(root.querySelector("table")).not.toBeNull();
    expect(root.textContent).toContain("const a = 1;");
    expect(root.querySelector('button[title^="Find in file"]')).not.toBeNull();

    // Verify line comment gutter interaction
    const gutter = root.querySelector('td[title="Click to comment on line 1"]') as HTMLElement;
    expect(gutter).not.toBeNull();
    gutter.click();
    await nextTick();

    expect(root.textContent).toContain("Comment · main.ts · line 1");
    expect(root.querySelector("textarea")).not.toBeNull();

    app.unmount();
  });

  it("renders A2UIRenderer for ui_manifest.json and supports dashboard/source toggling", async () => {
    const manifestJson = JSON.stringify({
      schemaVersion: "1.0",
      title: "Workspace Financial Hub",
      kpis: [
        {
          id: "k1",
          label: "Net Capital",
          value: 750000,
          format: "currency",
        },
      ],
      tabs: [
        {
          id: "t1",
          label: "Overview",
          widgets: [
            {
              id: "w1",
              type: "key-val-list",
              title: "Capital Distribution",
              items: [{ label: "Growth Fund", value: 400000, format: "currency" }],
            },
          ],
        },
      ],
    });

    vi.spyOn(api, "getFileContent").mockResolvedValueOnce({
      path: "/data/output/ui_manifest.json",
      name: "ui_manifest.json",
      ext: "json",
      size: manifestJson.length,
      content: manifestJson,
      isBinary: false,
      updatedAt: "2026-08-31T10:00:00Z",
    });

    const app = createApp({
      render() {
        return h(FileCodeViewer, {
          sessionId: "sess-abc",
          filePath: "/data/output/ui_manifest.json",
          comments: new Map(),
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.textContent).toContain("Workspace Financial Hub");
      expect(root.textContent).toContain("Net Capital");
      expect(root.textContent).toContain("$750,000.00");
    });

    // Check dashboard toggle button exists in toolbar
    const sourceBtn = Array.from(root.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("Source"),
    );
    expect(sourceBtn).toBeDefined();

    // Click Source button to view code table
    sourceBtn?.click();

    await vi.waitFor(() => {
      expect(root.querySelector("table")).not.toBeNull();
    });

    app.unmount();
  });
});
