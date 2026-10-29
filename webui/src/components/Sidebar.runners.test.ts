// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import Sidebar from "./Sidebar.vue";
import { createMemoryStorage } from "../test/memoryStorage";
import { i18n, setLocale } from "../i18n";
import * as api from "../lib/api";
import type { RunnerInfo } from "../types";
import { resetRunnersForTest, SELECTED_RUNNER_KEY } from "../composables/useRunners";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/chat/123" }),
  useRouter: () => ({ push: vi.fn<() => void>() }),
}));

const flush = async () => {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
    await nextTick();
  }
};

describe("Sidebar runner dropdown", () => {
  let root: HTMLElement;
  let app: ReturnType<typeof createApp>;

  const mount = async (runners: RunnerInfo[]) => {
    vi.spyOn(api, "getRunners").mockResolvedValue({ backend_version: "v1", runners });
    app = createApp({
      render: () => h(Sidebar, { sessions: [], activeSessionId: null, isOpen: true }),
    });
    app.use(i18n);
    app.mount(root);
    await flush();
  };

  beforeEach(() => {
    setLocale("en", false);
    vi.stubGlobal("localStorage", createMemoryStorage());
    resetRunnersForTest();
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    app?.unmount();
    document.body.removeChild(root);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("is hidden in legacy mode (no runners)", async () => {
    await mount([]);
    expect(root.querySelector('[data-testid="runner-select"]')).toBeNull();
  });

  it("defaults to the first online runner and lists runners with status dots", async () => {
    await mount([
      { node_id: "alpha", online: false },
      { node_id: "beta", online: true },
    ]);
    const btn = root.querySelector<HTMLButtonElement>('[data-testid="runner-select-button"]')!;
    expect(btn.textContent).toContain("beta");
    expect(root.querySelector('[data-testid="runner-selected-dot"]')!.className).toContain(
      "bg-success",
    );
    btn.click();
    await nextTick();
    const options = root.querySelectorAll('[data-testid^="runner-option-"]');
    expect(options.length).toBe(2);
    expect(options[0].querySelector("span")!.className).toContain("bg-error");
    expect(options[1].querySelector("span")!.className).toContain("bg-success");
  });

  it("persists the selection to localStorage and restores it", async () => {
    await mount([
      { node_id: "alpha", online: true },
      { node_id: "beta", online: true },
    ]);
    root.querySelector<HTMLButtonElement>('[data-testid="runner-select-button"]')!.click();
    await nextTick();
    root.querySelector<HTMLButtonElement>('[data-testid="runner-option-beta"]')!.click();
    await nextTick();
    expect(localStorage.getItem(SELECTED_RUNNER_KEY)).toBe("beta");
    expect(root.querySelector('[data-testid="runner-select-button"]')!.textContent).toContain(
      "beta",
    );
    expect(root.querySelector('[data-testid="runner-menu"]')).toBeNull();

    app.unmount();
    resetRunnersForTest();
    await mount([
      { node_id: "alpha", online: true },
      { node_id: "beta", online: true },
    ]);
    expect(root.querySelector('[data-testid="runner-select-button"]')!.textContent).toContain(
      "beta",
    );
  });

  it("disables offline options, falls back from an offline stored choice, and closes on Escape", async () => {
    localStorage.setItem(SELECTED_RUNNER_KEY, "alpha");
    resetRunnersForTest();
    await mount([
      { node_id: "alpha", online: false },
      { node_id: "beta", online: true },
    ]);
    const btn = root.querySelector<HTMLButtonElement>('[data-testid="runner-select-button"]')!;
    expect(btn.textContent).toContain("beta");
    btn.click();
    await nextTick();
    expect(
      root.querySelector<HTMLButtonElement>('[data-testid="runner-option-alpha"]')!.disabled,
    ).toBe(true);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await nextTick();
    expect(root.querySelector('[data-testid="runner-menu"]')).toBeNull();
  });
});
