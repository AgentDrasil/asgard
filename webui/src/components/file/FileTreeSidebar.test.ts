// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import FileTreeSidebar from "./FileTreeSidebar.vue";
import * as api from "../../lib/api";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("FileTreeSidebar.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders dropdown selector when runDir is separate from /tmp", async () => {
    const getTreeSpy = vi
      .spyOn(api, "getFileTree")
      .mockResolvedValue([{ name: "main.go", path: "main.go", isDir: false }]);

    const app = createApp({
      render() {
        return h(FileTreeSidebar, {
          sessionId: "sess-123",
          runDir: "/home/user/project",
          selectedPath: null,
          commentedFiles: [],
        });
      },
    });

    app.mount(root);
    await nextTick();
    await nextTick();

    expect(getTreeSpy).toHaveBeenCalledWith("sess-123", "");

    const selectEl = root.querySelector("select");
    expect(selectEl).not.toBeNull();
    const options = root.querySelectorAll("option");
    expect(options).toHaveLength(3);
    expect(options[0].textContent).toContain("project");
    expect(options[1].textContent).toContain("/tmp");
    expect(options[2].textContent).toContain("/session");
  });

  it("does not render dropdown selector when runDir is already /tmp/session-id (no duplicates)", async () => {
    const getTreeSpy = vi
      .spyOn(api, "getFileTree")
      .mockResolvedValue([{ name: "test.txt", path: "test.txt", isDir: false }]);

    const app = createApp({
      render() {
        return h(FileTreeSidebar, {
          sessionId: "sess-123",
          runDir: "/tmp/session-id",
          selectedPath: null,
          commentedFiles: [],
        });
      },
    });

    app.mount(root);
    await nextTick();
    await nextTick();

    expect(getTreeSpy).toHaveBeenCalledWith("sess-123", "");

    const selectEl = root.querySelector("select");
    expect(selectEl).toBeNull();
  });

  it("switches to /tmp scope when user selects /tmp option in dropdown", async () => {
    const getTreeSpy = vi
      .spyOn(api, "getFileTree")
      .mockResolvedValueOnce([{ name: "main.go", path: "main.go", isDir: false }])
      .mockResolvedValueOnce([{ name: "temp.log", path: "/tmp/temp.log", isDir: false }]);

    const app = createApp({
      render() {
        return h(FileTreeSidebar, {
          sessionId: "sess-123",
          runDir: "/home/user/project",
          selectedPath: null,
          commentedFiles: [],
        });
      },
    });

    app.mount(root);
    await nextTick();
    await nextTick();

    const selectEl = root.querySelector("select") as HTMLSelectElement;
    expect(selectEl).not.toBeNull();

    selectEl.value = "tmp";
    selectEl.dispatchEvent(new Event("change"));
    await nextTick();
    await nextTick();

    expect(getTreeSpy).toHaveBeenCalledWith("sess-123", "/tmp");
  });

  it("switches to /session scope when user selects /session option in dropdown", async () => {
    const getTreeSpy = vi
      .spyOn(api, "getFileTree")
      .mockResolvedValueOnce([{ name: "main.go", path: "main.go", isDir: false }])
      .mockResolvedValueOnce([{ name: "notes.md", path: "/session/notes.md", isDir: false }]);

    const app = createApp({
      render() {
        return h(FileTreeSidebar, {
          sessionId: "sess-123",
          runDir: "/home/user/project",
          selectedPath: null,
          commentedFiles: [],
        });
      },
    });

    app.mount(root);
    await nextTick();
    await nextTick();

    const selectEl = root.querySelector("select") as HTMLSelectElement;
    expect(selectEl).not.toBeNull();

    selectEl.value = "session";
    selectEl.dispatchEvent(new Event("change"));
    await nextTick();
    await nextTick();

    expect(getTreeSpy).toHaveBeenCalledWith("sess-123", "/session");
  });

  it("auto-switches to session scope when selectedPath points into /session", async () => {
    const getTreeSpy = vi
      .spyOn(api, "getFileTree")
      .mockResolvedValue([{ name: "notes.md", path: "/session/notes.md", isDir: false }]);

    const app = createApp({
      render() {
        return h(FileTreeSidebar, {
          sessionId: "sess-123",
          runDir: "/home/user/project",
          selectedPath: "/session/notes.md",
          commentedFiles: [],
        });
      },
    });

    app.mount(root);
    await nextTick();
    await nextTick();

    expect(getTreeSpy).toHaveBeenCalledWith("sess-123", "/session");
  });
});
