// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import FileSearchModal from "./FileSearchModal.vue";
import * as api from "../../lib/api";
import { i18n } from "../../i18n";
import type { FileSearchResult } from "../../types";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("FileSearchModal.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    vi.useFakeTimers();
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
    document.body.removeChild(root);
  });

  const mountWithI18n = (props: Record<string, any>) => {
    const app = createApp({
      render() {
        return h(FileSearchModal as any, props);
      },
    });
    app.use(i18n);
    app.mount(root);
    return app;
  };

  it("renders search input and triggers file search on input with debounce", async () => {
    const mockFiles: FileSearchResult[] = [
      { path: "src/main.ts", name: "main.ts", ext: "ts", size: 1024, scope: "workspace" },
      { path: "/tmp/debug.log", name: "debug.log", ext: "log", size: 2048, scope: "tmp" },
    ];
    const searchSpy = vi.spyOn(api, "searchFiles").mockResolvedValue(mockFiles);

    const app = mountWithI18n({
      isOpen: true,
      sessionId: "sess-test",
    });
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    expect(input).not.toBeNull();

    input.value = "main";
    input.dispatchEvent(new Event("input"));
    await nextTick();

    // Default debounceMs is 200ms in useFileSearchState
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    expect(searchSpy).toHaveBeenCalledWith("sess-test", "main", 50, expect.any(AbortSignal));

    await nextTick();
    const resultItems = root.querySelectorAll(".cursor-pointer");
    expect(resultItems.length).toBe(2);

    expect(root.textContent).toContain("main.ts");
    expect(root.textContent).toContain("debug.log");

    // Check tmp badge rendering
    const tmpBadges = root.querySelectorAll(".badge");
    expect(tmpBadges.length).toBe(1);
    expect(tmpBadges[0].textContent?.trim()).toBe("tmp");

    app.unmount();
  });

  it("emits select-file event with path and scope on item click", async () => {
    const mockFiles: FileSearchResult[] = [
      { path: "/tmp/result.json", name: "result.json", ext: "json", size: 512, scope: "tmp" },
    ];
    vi.spyOn(api, "searchFiles").mockResolvedValue(mockFiles);

    let selectedPath = "";
    let selectedScope: string | undefined = "";
    let closed = false;

    const app = mountWithI18n({
      isOpen: true,
      sessionId: "sess-test",
      "onSelect-file": (path: string, scope?: string) => {
        selectedPath = path;
        selectedScope = scope;
      },
      onClose: () => {
        closed = true;
      },
    });
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "result";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    const item = root.querySelector(".cursor-pointer") as HTMLElement;
    expect(item).not.toBeNull();
    item.click();
    await nextTick();

    expect(selectedPath).toBe("/tmp/result.json");
    expect(selectedScope).toBe("tmp");
    expect(closed).toBe(true);

    app.unmount();
  });

  it("supports keyboard navigation and enter selection with scope", async () => {
    const mockFiles: FileSearchResult[] = [
      { path: "src/app.vue", name: "app.vue", ext: "vue", size: 100, scope: "workspace" },
      { path: "/tmp/output.txt", name: "output.txt", ext: "txt", size: 200, scope: "tmp" },
    ];
    vi.spyOn(api, "searchFiles").mockResolvedValue(mockFiles);

    let selectedPath = "";
    let selectedScope: string | undefined = "";

    const app = mountWithI18n({
      isOpen: true,
      sessionId: "sess-test",
      "onSelect-file": (path: string, scope?: string) => {
        selectedPath = path;
        selectedScope = scope;
      },
    });
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "test";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    // Navigate down to 2nd item
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }));
    await nextTick();

    // Enter to select
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    await nextTick();

    expect(selectedPath).toBe("/tmp/output.txt");
    expect(selectedScope).toBe("tmp");

    app.unmount();
  });

  it("emits close event on Escape key", async () => {
    let closed = false;

    const app = mountWithI18n({
      isOpen: true,
      sessionId: "sess-test",
      onClose: () => {
        closed = true;
      },
    });
    await nextTick();

    const dialog = root.querySelector("dialog") as HTMLDialogElement;
    dialog.dispatchEvent(new Event("cancel", { cancelable: true }));
    dialog.close();
    await nextTick();

    expect(closed).toBe(true);

    app.unmount();
  });

  it("exposes combobox/listbox ARIA with active descendant and live count", async () => {
    vi.spyOn(api, "searchFiles").mockResolvedValue([
      { path: "src/a.ts", name: "a.ts", ext: "ts", size: 1, scope: "workspace" },
      { path: "src/b.ts", name: "b.ts", ext: "ts", size: 2, scope: "workspace" },
    ]);
    const app = mountWithI18n({ isOpen: true, sessionId: "sess-test" });
    await nextTick();
    const input = root.querySelector("input") as HTMLInputElement;
    expect(input.getAttribute("role")).toBe("combobox");
    expect(input.getAttribute("aria-autocomplete")).toBe("list");
    expect(input.getAttribute("aria-expanded")).toBe("false");
    input.value = "ts";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();
    const listbox = root.querySelector('[role="listbox"]') as HTMLElement;
    expect(listbox.id).toBe(input.getAttribute("aria-controls"));
    expect(input.getAttribute("aria-expanded")).toBe("true");
    const options = root.querySelectorAll('[role="option"]');
    expect(options.length).toBe(2);
    expect(options[0].getAttribute("aria-selected")).toBe("true");
    expect(input.getAttribute("aria-activedescendant")).toBe(options[0].id);
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }));
    await nextTick();
    expect(input.getAttribute("aria-activedescendant")).toBe(options[1].id);
    expect(options[1].getAttribute("aria-selected")).toBe("true");
    expect(new Set([...options].map((o) => o.id)).size).toBe(2);
    expect(root.querySelector('[aria-live="polite"]')?.textContent).toContain("2 files available");
    app.unmount();
  });

  const mountFileAt = (target: HTMLElement) => {
    const app = createApp({
      render() {
        return h(FileSearchModal as any, { isOpen: true, sessionId: "sess-test" });
      },
    });
    app.use(i18n);
    app.mount(target);
    return app;
  };

  it("announces no results and drops activedescendant when empty", async () => {
    const spy = vi.spyOn(api, "searchFiles");
    spy.mockResolvedValueOnce([
      { path: "src/a.ts", name: "a.ts", ext: "ts", size: 1, scope: "workspace" },
      { path: "src/b.ts", name: "b.ts", ext: "ts", size: 2, scope: "workspace" },
    ]);
    const app = mountFileAt(root);
    await nextTick();
    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "ts";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();
    expect(root.querySelectorAll('[role="option"]').length).toBe(2);
    expect(input.hasAttribute("aria-activedescendant")).toBe(true);

    spy.mockResolvedValueOnce([]);
    input.value = "zzz";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();
    expect(root.querySelectorAll('[role="option"]').length).toBe(0);
    expect(input.getAttribute("aria-expanded")).toBe("false");
    expect(input.hasAttribute("aria-activedescendant")).toBe(false);
    expect(root.querySelector('[aria-live="polite"]')?.textContent).toContain("No files found");
    app.unmount();
  });

  it("uses non-colliding ids across two mounted instances", async () => {
    vi.spyOn(api, "searchFiles").mockResolvedValue([
      { path: "src/a.ts", name: "a.ts", ext: "ts", size: 1, scope: "workspace" },
      { path: "src/b.ts", name: "b.ts", ext: "ts", size: 2, scope: "workspace" },
    ]);
    // Two instances inside one app share useId's counter, as in production.
    const app = createApp({
      render() {
        return h("div", [
          h("div", { id: "a" }, [
            h(FileSearchModal as any, { isOpen: true, sessionId: "sess-test" }),
          ]),
          h("div", { id: "b" }, [
            h(FileSearchModal as any, { isOpen: true, sessionId: "sess-test" }),
          ]),
        ]);
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();
    const inputs = [...root.querySelectorAll("input")] as HTMLInputElement[];
    expect(inputs.length).toBe(2);
    for (const input of inputs) {
      input.value = "ts";
      input.dispatchEvent(new Event("input"));
    }
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();
    const ids = [...root.querySelectorAll("[id]")]
      .map((e) => e.id)
      .filter((id) => id.includes("-option-") || id.includes("-listbox"));
    expect(ids.length).toBe(6);
    expect(new Set(ids).size).toBe(6);
    expect(inputs[0].getAttribute("aria-controls")).not.toBe(
      inputs[1].getAttribute("aria-controls"),
    );
    app.unmount();
  });
});
