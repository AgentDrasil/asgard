// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h } from "vue";
import ArtifactViewer from "./ArtifactViewer.vue";

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
});
