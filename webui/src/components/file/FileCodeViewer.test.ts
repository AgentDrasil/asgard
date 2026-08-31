// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import FileCodeViewer from "./FileCodeViewer.vue";
import * as api from "../../lib/api";

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
    app.mount(root);

    await new Promise((r) => setTimeout(r, 50));
    await nextTick();

    const mediaViewer = root.querySelector(".media-viewer");
    expect(mediaViewer).not.toBeNull();
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
    app.mount(root);

    await new Promise((r) => setTimeout(r, 50));
    await nextTick();

    const mediaViewer = root.querySelector(".media-viewer");
    expect(mediaViewer).not.toBeNull();
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
    app.mount(root);

    await new Promise((r) => setTimeout(r, 50));
    await nextTick();

    const mediaViewer = root.querySelector(".media-viewer");
    expect(mediaViewer).not.toBeNull();
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
    app.mount(root);

    await new Promise((r) => setTimeout(r, 50));
    await nextTick();

    expect(root.querySelector(".media-viewer")).toBeNull();
    expect(root.querySelector("table")).not.toBeNull();
    expect(root.textContent).toContain("console.log('hello');");
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
});
