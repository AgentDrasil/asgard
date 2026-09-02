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

    const input = root.querySelector("input") as HTMLInputElement;
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await nextTick();

    expect(closed).toBe(true);

    app.unmount();
  });
});
