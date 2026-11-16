// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h } from "vue";
import ArtifactViewer from "./ArtifactViewer.vue";
import { i18n } from "../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock useShiki
vi.mock("../composables/useShiki", () => ({
  useShiki: () => ({
    highlightBlock: (content: string, _lang: string) =>
      `<pre class="shiki"><code>${content}</code></pre>`,
    highlightHtmlCodeBlocks: (html: string) => html,
    highlightToHtml: (_code: string, _lang: string) => "",
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

describe("ArtifactViewer.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders MediaViewer for image artifact file", async () => {
    // Mock fetch for image file
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/diagram.png",
        name: "diagram.png",
        ext: "png",
        size: 2048,
        content: "",
        isBinary: true,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/diagram.png",
          modifiedFiles: ["/workspace/diagram.png"],
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
      "/api/v1/workspace/file?session_id=sess-123&path=%2Fworkspace%2Fdiagram.png&raw=1",
    );
    expect(root.textContent).toContain("diagram.png");

    // In-page find button should be hidden for media
    expect(root.querySelector('button[title="Find in artifact"]')).toBeNull();

    app.unmount();
  });

  it("renders CsvViewer for CSV artifact file and supports fullscreen toggle", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/data.csv",
        name: "data.csv",
        ext: "csv",
        size: 100,
        content: "Name,Score\nAlice,95\nBob,80",
        isBinary: false,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    let expandToggled = false;
    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/data.csv",
          modifiedFiles: ["/workspace/data.csv"],
          isExpanded: false,
          onToggleExpand: () => {
            expandToggled = true;
          },
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
    expect(root.textContent).toContain("95");

    const expandBtn = root.querySelector('button[title^="Full screen"]') as HTMLButtonElement;
    expect(expandBtn).not.toBeNull();
    expandBtn.click();
    expect(expandToggled).toBe(true);

    app.unmount();
  });

  it("renders MediaViewer for video artifact file", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/demo.mp4",
        name: "demo.mp4",
        ext: "mp4",
        size: 1048576,
        content: "",
        isBinary: true,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/demo.mp4",
          modifiedFiles: ["/workspace/demo.mp4"],
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector("video")).not.toBeNull();
    });

    const video = root.querySelector("video");
    expect(video).not.toBeNull();
    expect(video?.getAttribute("src")).toBe(
      "/api/v1/workspace/file?session_id=sess-123&path=%2Fworkspace%2Fdemo.mp4&raw=1",
    );

    app.unmount();
  });

  it("renders MediaViewer for audio artifact file", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/track.mp3",
        name: "track.mp3",
        ext: "mp3",
        size: 524288,
        content: "",
        isBinary: true,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/track.mp3",
          modifiedFiles: ["/workspace/track.mp3"],
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector("audio")).not.toBeNull();
    });

    const audio = root.querySelector("audio");
    expect(audio).not.toBeNull();
    expect(audio?.getAttribute("src")).toBe(
      "/api/v1/workspace/file?session_id=sess-123&path=%2Fworkspace%2Ftrack.mp3&raw=1",
    );

    app.unmount();
  });

  it("renders MediaViewer for PDF artifact file", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/report.pdf",
        name: "report.pdf",
        ext: "pdf",
        size: 8192,
        content: "",
        isBinary: true,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/report.pdf",
          modifiedFiles: ["/workspace/report.pdf"],
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector(".media-viewer")).not.toBeNull();
    });

    expect(root.querySelector("iframe")).not.toBeNull();
    expect(root.querySelector('a[title="Open in new window / Download"]')).not.toBeNull();

    app.unmount();
  });

  it("renders MarkdownContent for markdown files", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/README.md",
        name: "README.md",
        ext: "md",
        size: 120,
        content: "# Hello World\nThis is markdown",
        isBinary: false,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/README.md",
          modifiedFiles: ["/workspace/README.md"],
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.textContent).toContain("Hello World");
    });

    expect(root.querySelector(".media-viewer")).toBeNull();
    expect(root.querySelector('button[title="Rendered Markdown Preview"]')).not.toBeNull();
    expect(root.querySelector('button[title="Find in artifact"]')).not.toBeNull();

    app.unmount();
  });

  it("renders code highlighter for source code files", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/main.go",
        name: "main.go",
        ext: "go",
        size: 200,
        content: "package main\n\nfunc main() {}",
        isBinary: false,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/main.go",
          modifiedFiles: ["/workspace/main.go"],
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.textContent).toContain("package main");
    });

    expect(root.querySelector(".media-viewer")).toBeNull();
    expect(root.querySelector("pre.shiki")).not.toBeNull();
    // Verify file icon uses correct extension icon (not fallback code icon)
    expect(root.querySelector('[data-icon="vscode-icons:file-type-go"]')).not.toBeNull();

    app.unmount();
  });

  it("renders MediaViewer binary fallback card for non-media binary artifact without dead CTA", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/app.wasm",
        name: "app.wasm",
        ext: "wasm",
        size: 65536,
        content: "",
        isBinary: true,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/app.wasm",
          modifiedFiles: ["/workspace/app.wasm"],
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
    // Header icon should be rendered
    expect(root.querySelector('[data-icon="octicon:file-code-24"]')).not.toBeNull();
    // Toolbar raw link should not be present for non-media binary
    expect(root.querySelector('a[title="Open in new window / Download"]')).toBeNull();

    app.unmount();
  });

  it("renders A2UIRenderer for ui_manifest.json artifact and allows toggling source", async () => {
    const manifestJson = JSON.stringify({
      schemaVersion: "1.0",
      title: "Wealth Dashboard",
      kpis: [
        {
          id: "k1",
          label: "Portfolio Value",
          value: 500000,
          format: "currency",
        },
      ],
      tabs: [
        {
          id: "t1",
          label: "Assets",
          widgets: [
            {
              id: "w1",
              type: "key-val-list",
              title: "Holdings Breakdown",
              items: [{ label: "Tech", value: 350000, format: "currency" }],
            },
          ],
        },
      ],
    });

    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/workspace/data/ui_manifest.json",
        name: "ui_manifest.json",
        ext: "json",
        size: manifestJson.length,
        content: manifestJson,
        isBinary: false,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "sess-123",
          activeFilePath: "/workspace/data/ui_manifest.json",
          modifiedFiles: ["/workspace/data/ui_manifest.json"],
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.textContent).toContain("Wealth Dashboard");
      expect(root.textContent).toContain("Portfolio Value");
      expect(root.textContent).toContain("$500,000.00");
    });

    // Check dashboard toggle button exists
    const sourceBtn = Array.from(root.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("Source"),
    );
    expect(sourceBtn).toBeDefined();

    // Click Source button to view raw code
    sourceBtn?.click();

    await vi.waitFor(() => {
      expect(root.querySelector("pre.shiki")).not.toBeNull();
    });

    app.unmount();
  });

  it("opens an artifact of a subagent with the subagent's session id and labels it", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/tmp/out/report.png",
        name: "report.png",
        ext: "png",
        size: 10,
        content: "",
        isBinary: true,
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);
    const key = "subagent:child-9:/tmp/out/report.png";

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "parent-1",
          activeFilePath: key,
          modifiedFiles: ["/tmp/own.txt", key],
          sources: { [key]: { chatId: "child-9", label: "tester", agent: "coder" } },
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(root.querySelector(".media-viewer")).not.toBeNull();
    });
    expect(String(fetchSpy.mock.calls[0][0])).toContain(
      "/api/v1/workspace/file?session_id=child-9&path=%2Ftmp%2Fout%2Freport.png",
    );
    expect(root.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/workspace/file?session_id=child-9&path=%2Ftmp%2Fout%2Freport.png&raw=1",
    );
    const options = Array.from(root.querySelectorAll("option")).map((o) => o.textContent?.trim());
    expect(options).toEqual(["/tmp/own.txt", "[tester] /tmp/out/report.png"]);

    app.unmount();
  });

  it("opens a /shared artifact with the viewed chat's session id", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        path: "/shared/plan.md",
        name: "plan.md",
        ext: "md",
        size: 5,
        content: "# plan",
        updatedAt: "2026-08-31T10:00:00Z",
      }),
    } as Response);

    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "parent-1",
          activeFilePath: "/shared/plan.md",
          modifiedFiles: ["/shared/plan.md"],
          sources: { "/shared/plan.md": { chatId: "child-9", label: "planner" } },
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    await vi.waitFor(() => {
      expect(fetchSpy).toHaveBeenCalled();
    });
    expect(String(fetchSpy.mock.calls[0][0])).toContain(
      "session_id=parent-1&path=%2Fshared%2Fplan.md",
    );
    await vi.waitFor(() => {
      expect(root.textContent).toContain("plan");
    });
    expect(root.querySelector("option")?.textContent?.trim()).toBe("[planner] /shared/plan.md");

    app.unmount();
  });
});
