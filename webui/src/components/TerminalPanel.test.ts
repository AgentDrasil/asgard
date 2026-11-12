// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick, reactive, type App } from "vue";
import TerminalPanel from "./TerminalPanel.vue";
import { i18n, setLocale } from "../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

vi.mock("./TerminalView.vue", () => ({
  default: { name: "TerminalView", template: `<div class="mock-terminal"></div>` },
}));

const STORAGE_KEY = "asgard_terminal_panel_height";
let app: App | null = null;

function setViewportHeight(h: number) {
  Object.defineProperty(window, "innerHeight", { value: h, configurable: true, writable: true });
}

function setViewport(desktop: boolean) {
  window.matchMedia = ((q: string) => ({
    matches: desktop,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

async function mountPanel(onHide: () => void = () => {}) {
  const props = reactive({ isOpen: false });
  const root = document.createElement("div");
  document.body.appendChild(root);
  app = createApp({
    render: () => h(TerminalPanel, { sessionId: "s1", isOpen: props.isOpen, onHide }),
  });
  app.use(i18n);
  app.mount(root);
  await nextTick();
  props.isOpen = true;
  await nextTick();
  await nextTick();
  await nextTick();
  const handle = () => root.querySelector('[role="separator"]') as HTMLElement | null;
  const panel = () => root.querySelector("div.border-t") as HTMLElement;
  return { root, handle, panel, props };
}

beforeEach(() => {
  const store = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  });
  setViewportHeight(1000);
  setLocale("en", false);
  setViewport(true);
});

afterEach(() => {
  app?.unmount();
  app = null;
  document.body.innerHTML = "";
});

describe("TerminalPanel resize", () => {
  it("double-click resets the height to the default and persists it", async () => {
    localStorage.setItem(STORAGE_KEY, "500");
    const { handle, panel } = await mountPanel();
    expect(panel().style.height).toBe("500px");
    handle()!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    await nextTick();
    expect(panel().style.height).toBe("320px");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("320");
  });

  it("clamps the restored height to the viewport maximum", async () => {
    localStorage.setItem(STORAGE_KEY, "900");
    const { handle, panel } = await mountPanel();
    expect(panel().style.height).toBe("750px");
    expect(handle()!.getAttribute("aria-valuemax")).toBe("750");
  });

  it("re-clamps and updates aria-valuemax when the viewport shrinks", async () => {
    localStorage.setItem(STORAGE_KEY, "700");
    const { handle, panel } = await mountPanel();
    expect(panel().style.height).toBe("700px");
    setViewportHeight(600);
    window.dispatchEvent(new Event("resize"));
    await nextTick();
    expect(handle()!.getAttribute("aria-valuemax")).toBe("450");
    expect(panel().style.height).toBe("450px");
  });

  it("removes the resize handle while maximized", async () => {
    const { root, handle } = await mountPanel();
    expect(handle()).not.toBeNull();
    const icon = root.querySelector('.mock-icon[data-icon="octicon:screen-full-24"]');
    (icon!.closest("button") as HTMLButtonElement).click();
    await nextTick();
    expect(handle()).toBeNull();
  });
});

describe("TerminalPanel.vue accessibility", () => {
  it("labels the docked desktop panel as a region (not modal)", async () => {
    setViewport(true);
    const { root } = await mountPanel();
    const el = root.querySelector('[role="region"]') as HTMLElement;
    expect(el).not.toBeNull();
    expect(el.getAttribute("aria-label")).toBe("Terminal (Agent Workspace)");
    expect(root.querySelector('[role="dialog"]')).toBeNull();
  });

  it("mobile overlay is a modal dialog; focuses in, restores focus, Esc hides", async () => {
    setViewport(false);
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    const onHide = vi.fn<() => void>();
    const { root, props } = await mountPanel(onHide);
    const dlg = root.querySelector('[role="dialog"]') as HTMLElement;
    expect(dlg.getAttribute("aria-modal")).toBe("true");
    expect(dlg.getAttribute("aria-label")).toBe("Terminal");
    expect(document.activeElement).toBe(dlg);

    dlg.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(onHide).toHaveBeenCalledTimes(1);

    // Esc inside the terminal belongs to the shell.
    const x = document.createElement("div");
    x.className = "xterm";
    dlg.appendChild(x);
    x.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(onHide).toHaveBeenCalledTimes(1);

    props.isOpen = false;
    await nextTick();
    await nextTick();
    expect(document.activeElement).toBe(opener);
  });
});
