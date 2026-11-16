// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, nextTick, h, ref } from "vue";
import FileTreeSidebar from "./FileTreeSidebar.vue";
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
    app.use(i18n);
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
    app.use(i18n);
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
    app.use(i18n);
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
    app.use(i18n);
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
    app.use(i18n);
    app.mount(root);
    await nextTick();
    await nextTick();

    expect(getTreeSpy).toHaveBeenCalledWith("sess-123", "/session");
  });

  it("offers /shared only for a chat of a spawn tree and browses it", async () => {
    const getTreeSpy = vi.spyOn(api, "getFileTree").mockResolvedValue([]);
    const mount = (hasShared: boolean, runDir: string, selectedPath: string | null = null) => {
      const el = document.createElement("div");
      document.body.appendChild(el);
      const app = createApp({
        render() {
          return h(FileTreeSidebar, {
            sessionId: "sess-123",
            runDir,
            selectedPath,
            commentedFiles: [],
            hasShared,
          });
        },
      });
      app.use(i18n);
      app.mount(el);
      return { el, app };
    };

    const plain = mount(false, "/home/user/project");
    await nextTick();
    expect(Array.from(plain.el.querySelectorAll("option")).map((o) => o.value)).toEqual([
      "workdir",
      "tmp",
      "session",
    ]);
    plain.app.unmount();

    const tree = mount(true, "/home/user/project");
    await nextTick();
    const select = tree.el.querySelector("select") as HTMLSelectElement;
    expect(Array.from(select.querySelectorAll("option")).map((o) => o.value)).toEqual([
      "workdir",
      "tmp",
      "session",
      "shared",
    ]);
    select.value = "shared";
    select.dispatchEvent(new Event("change"));
    await nextTick();
    await nextTick();
    expect(getTreeSpy).toHaveBeenLastCalledWith("sess-123", "/shared");
    tree.app.unmount();

    // A run dir that is the chat's tmp has no selector, unless there is /shared.
    const inTmp = mount(true, "/tmp/session-id", "/shared/out/a.md");
    await nextTick();
    await nextTick();
    const opts = Array.from(inTmp.el.querySelectorAll("option")).map((o) => o.value);
    expect(opts).toEqual(["workdir", "shared"]);
    expect(getTreeSpy).toHaveBeenLastCalledWith("sess-123", "/shared");
    inTmp.app.unmount();
  });

  it("reacts to hasShared changing after mount", async () => {
    const getTreeSpy = vi.spyOn(api, "getFileTree").mockResolvedValue([]);
    const hasShared = ref(false);
    const app = createApp({
      render() {
        return h(FileTreeSidebar, {
          sessionId: "sess-123",
          runDir: "/tmp/session-id",
          selectedPath: "/shared/out/a.md",
          commentedFiles: [],
          hasShared: hasShared.value,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();
    // No /shared yet: no selector, the workspace is listed.
    expect(root.querySelector("select")).toBeNull();
    expect(getTreeSpy).toHaveBeenLastCalledWith("sess-123", "");

    // The first subagent appears: /shared is offered and the selected path's
    // scope is followed.
    hasShared.value = true;
    await nextTick();
    await nextTick();
    await nextTick();
    expect(root.querySelector("select")).not.toBeNull();
    expect(getTreeSpy).toHaveBeenLastCalledWith("sess-123", "/shared");

    hasShared.value = false;
    await nextTick();
    await nextTick();
    await nextTick();
    expect(root.querySelector("select")).toBeNull();
    expect(getTreeSpy).toHaveBeenLastCalledWith("sess-123", "");
    app.unmount();
  });
});
