// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h, ref } from "vue";
import TerminalPanel from "./TerminalPanel.vue";
import { i18n, setLocale } from "../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: { name: "Icon", props: ["icon"], template: `<span></span>` },
}));
vi.mock("./TerminalView.vue", () => ({
  default: { name: "TerminalView", template: `<div class="xterm"></div>` },
}));

function setViewport(desktop: boolean) {
  window.matchMedia = ((q: string) => ({
    matches: desktop,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

describe("TerminalPanel.vue accessibility", () => {
  let root: HTMLElement;
  beforeEach(() => {
    vi.stubGlobal("localStorage", {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {},
    });
    setLocale("en", false);
    root = document.createElement("div");
    document.body.appendChild(root);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.removeChild(root);
  });

  function mount(isOpen: ReturnType<typeof ref<boolean>>, onHide: () => void = () => {}) {
    const app = createApp({
      render() {
        return h(TerminalPanel, {
          sessionId: "s1",
          isOpen: isOpen.value as boolean,
          onHide,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    return app;
  }

  it("labels the docked desktop panel as a region (not modal)", async () => {
    setViewport(true);
    const open = ref(false);
    const app = mount(open);
    await nextTick();
    open.value = true;
    await nextTick();
    await nextTick();
    const el = root.querySelector('[role="region"]') as HTMLElement;
    expect(el).not.toBeNull();
    expect(el.getAttribute("aria-label")).toBe("Terminal (Agent Workspace)");
    expect(root.querySelector('[role="dialog"]')).toBeNull();
    app.unmount();
  });

  it("mobile overlay is a modal dialog; focuses in, restores focus, Esc hides", async () => {
    setViewport(false);
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    const open = ref(false);
    const onHide = vi.fn<() => void>();
    const app = mount(open, onHide);
    await nextTick();
    open.value = true;
    await nextTick();
    await nextTick();
    await nextTick();
    const dlg = root.querySelector('[role="dialog"]') as HTMLElement;
    expect(dlg.getAttribute("aria-modal")).toBe("true");
    expect(dlg.getAttribute("aria-label")).toBe("Terminal");
    expect(document.activeElement).toBe(dlg);

    dlg.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(onHide).toHaveBeenCalledTimes(1);

    // Esc inside the terminal belongs to the shell.
    const x = dlg.querySelector(".xterm") as HTMLElement;
    expect(x).not.toBeNull();
    x.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(onHide).toHaveBeenCalledTimes(1);

    open.value = false;
    await nextTick();
    await nextTick();
    expect(document.activeElement).toBe(opener);
    app.unmount();
    document.body.removeChild(opener);
  });
});
