// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h } from "vue";
import ArtifactViewer from "./ArtifactViewer.vue";
import { i18n } from "../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: { name: "Icon", props: ["icon"], template: `<span class="mock-icon"></span>` },
}));
vi.mock("../composables/useShiki", () => ({
  useShiki: () => ({
    highlightBlock: (content: string) => `<pre class="shiki"><code>${content}</code></pre>`,
    highlightHtmlCodeBlocks: (html: string) => html,
    highlightToHtml: () => "",
  }),
}));
// The renderer's data sources load through the session id it is given.
vi.mock("./a2ui/A2UIRenderer.vue", () => ({
  default: {
    name: "A2UIRenderer",
    props: ["manifest", "sessionId", "manifestPath"],
    template: `<div class="a2ui-stub" :data-session="sessionId" :data-path="manifestPath"></div>`,
  },
}));

const manifest = JSON.stringify({
  schemaVersion: "1.0",
  title: "Child Dashboard",
  kpis: [{ id: "k1", label: "K", value: 1 }],
  tabs: [{ id: "t", label: "T", widgets: [] }],
});

function fileReply(path: string, content: string, ext: string) {
  return {
    ok: true,
    json: async () => ({
      path,
      name: path.split("/").pop(),
      ext,
      size: content.length,
      content,
      isBinary: false,
      updatedAt: "2026-08-31T10:00:00Z",
    }),
  } as Response;
}

describe("ArtifactViewer.vue artifact sources", () => {
  let root: HTMLElement;
  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("loads a child's A2UI dashboard data sources from the child chat", async () => {
    const key = "subagent:child-9:/tmp/ui_manifest.json";
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(fileReply("/tmp/ui_manifest.json", manifest, "json"));
    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "parent-1",
          activeFilePath: key,
          modifiedFiles: [key],
          sources: { [key]: { chatId: "child-9", label: "tester" } },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await vi.waitFor(() => {
      expect(root.querySelector(".a2ui-stub")).not.toBeNull();
    });
    expect(String(fetchSpy.mock.calls[0][0])).toContain("session_id=child-9");
    const stub = root.querySelector(".a2ui-stub") as HTMLElement;
    expect(stub.dataset.session).toBe("child-9");
    expect(stub.dataset.path).toBe("/tmp/ui_manifest.json");
    app.unmount();
  });

  it("does not follow a workspace file named like a subagent key", async () => {
    // Not in `sources`: it is just a path of the viewed chat.
    const key = "subagent:other-chat:/tmp/x";
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(fileReply(key, "plain", "txt"));
    const app = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "parent-1",
          activeFilePath: key,
          modifiedFiles: [key],
          sources: {},
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await vi.waitFor(() => {
      expect(fetchSpy).toHaveBeenCalled();
    });
    const url = String(fetchSpy.mock.calls[0][0]);
    expect(url).toContain("session_id=parent-1");
    expect(url).toContain(`path=${encodeURIComponent(key)}`);
    expect(root.querySelector("option")?.textContent?.trim()).toBe(key);

    // A source of another chat for the same key does not redirect it either.
    app.unmount();
    fetchSpy.mockResolvedValueOnce(fileReply(key, "plain", "txt"));
    const app2 = createApp({
      render() {
        return h(ArtifactViewer, {
          sessionId: "parent-1",
          activeFilePath: key,
          modifiedFiles: [key],
          sources: { [key]: { chatId: "someone-else", label: "x" } },
        });
      },
    });
    app2.use(i18n);
    app2.mount(root);
    await vi.waitFor(() => {
      expect(fetchSpy).toHaveBeenCalledTimes(2);
    });
    expect(String(fetchSpy.mock.calls[1][0])).toContain("session_id=parent-1");
    app2.unmount();
  });
});
