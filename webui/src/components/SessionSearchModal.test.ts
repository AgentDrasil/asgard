// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import SessionSearchModal from "./SessionSearchModal.vue";
import { i18n, setLocale } from "../i18n";
import * as api from "../lib/api";
import type { ChatSession } from "../types";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("SessionSearchModal.vue", () => {
  let root: HTMLElement;

  const mockSessions: ChatSession[] = [
    {
      chatID: "101",
      title: "Fix login bug",
      currentAgent: "asgard",
      runDir: "/workspace/project-a",
      isRunning: false,
    },
    {
      chatID: "202",
      title: "Refactor API",
      currentAgent: "dev-agent",
      runDir: "/tmp/sandbox",
      isRunning: true,
    },
  ];

  beforeEach(() => {
    vi.useFakeTimers();
    setLocale("en", false);
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
    document.body.removeChild(root);
  });

  it("renders input and initial empty state when opened", async () => {
    const searchSpy = vi.spyOn(api, "searchSessions");

    const app = createApp({
      render() {
        return h(SessionSearchModal, { isOpen: true });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    expect(input).not.toBeNull();

    // No search issued before typing
    expect(searchSpy).not.toHaveBeenCalled();

    // Initial placeholder state, no session list items
    expect(root.textContent).toContain("Type to search sessions");
    expect(root.querySelectorAll(".cursor-pointer").length).toBe(0);

    app.unmount();
  });

  it("debounces input and renders session results", async () => {
    const searchSpy = vi.spyOn(api, "searchSessions").mockResolvedValue(mockSessions);

    const app = createApp({
      render() {
        return h(SessionSearchModal, { isOpen: true });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "fix";
    input.dispatchEvent(new Event("input"));
    await nextTick();

    // Default debounceMs is 200ms in useSessionSearchState
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    expect(searchSpy).toHaveBeenCalledWith("fix", expect.any(AbortSignal));

    await nextTick();
    const resultItems = root.querySelectorAll(".cursor-pointer");
    expect(resultItems.length).toBe(2);

    expect(root.textContent).toContain("Fix login bug");
    expect(root.textContent).toContain("Refactor API");
    expect(root.textContent).toContain("asgard");
    expect(root.textContent).toContain("dev-agent");

    // Running badge only for running session
    const runningBadges = Array.from(root.querySelectorAll(".badge")).filter(
      (el) => el.textContent?.trim() === "running",
    );
    expect(runningBadges.length).toBe(1);

    app.unmount();
  });

  it("emits select-session and close events on item click", async () => {
    vi.spyOn(api, "searchSessions").mockResolvedValue(mockSessions);

    let selected: ChatSession | null = null;
    let closed = false;

    const app = createApp({
      render() {
        return h(SessionSearchModal, {
          isOpen: true,
          "onSelect-session": (session: ChatSession) => {
            selected = session;
          },
          onClose: () => {
            closed = true;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "fix";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    const item = root.querySelector(".cursor-pointer") as HTMLElement;
    expect(item).not.toBeNull();
    item.click();
    await nextTick();

    expect(selected).not.toBeNull();
    expect((selected as ChatSession | null)?.chatID).toBe("101");
    expect(closed).toBe(true);

    app.unmount();
  });

  it("supports keyboard navigation and enter selection", async () => {
    vi.spyOn(api, "searchSessions").mockResolvedValue(mockSessions);

    let selected: ChatSession | null = null;

    const app = createApp({
      render() {
        return h(SessionSearchModal, {
          isOpen: true,
          "onSelect-session": (session: ChatSession) => {
            selected = session;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "api";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    // Navigate down to 2nd item
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }));
    await nextTick();

    // Navigate up back to 1st item
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp" }));
    await nextTick();

    // Enter to select current (1st item)
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    await nextTick();

    expect(selected).not.toBeNull();
    expect((selected as ChatSession | null)?.chatID).toBe("101");

    app.unmount();
  });

  it("emits close event on Escape key", async () => {
    let closed = false;

    const app = createApp({
      render() {
        return h(SessionSearchModal, {
          isOpen: true,
          onClose: () => {
            closed = true;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await nextTick();

    expect(closed).toBe(true);

    app.unmount();
  });

  it("renders empty results message when no sessions match query", async () => {
    vi.spyOn(api, "searchSessions").mockResolvedValue([]);

    const app = createApp({
      render() {
        return h(SessionSearchModal, { isOpen: true });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "nonexistent";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    expect(root.textContent).toContain('No sessions found matching "nonexistent"');
    expect(root.querySelectorAll(".cursor-pointer").length).toBe(0);

    app.unmount();
  });

  it("renders error state when search fails", async () => {
    vi.spyOn(api, "searchSessions").mockRejectedValue(new Error("Database offline"));

    const app = createApp({
      render() {
        return h(SessionSearchModal, { isOpen: true });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    input.value = "fail";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    await vi.advanceTimersByTimeAsync(250);
    await nextTick();

    expect(root.querySelector(".alert-error")).not.toBeNull();
    expect(root.textContent).toContain("Database offline");

    app.unmount();
  });

  it("renders localized UI in Chinese", async () => {
    setLocale("zh-CN", false);

    const app = createApp({
      render() {
        return h(SessionSearchModal, { isOpen: true });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    expect(input.placeholder).toBe("按标题、Agent 或工作区搜索会话...");
    expect(root.textContent).toContain("输入关键词搜索会话");

    app.unmount();
  });
});
