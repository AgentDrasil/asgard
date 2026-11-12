// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import MermaidViewer from "./MermaidViewer.vue";
import { i18n, setLocale } from "../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

vi.mock("../composables/useMermaid", async () => {
  const { ref } = await import("vue");
  return {
    useMermaid: () => ({
      activeMermaidTheme: ref("default"),
      renderDiagram: vi
        .fn<() => Promise<unknown>>()
        .mockResolvedValue({ svg: "<svg></svg>", bindFunctions: undefined }),
    }),
  };
});

describe("MermaidViewer.vue fullscreen", () => {
  let root: HTMLElement;

  beforeEach(() => {
    setLocale("en", false);
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    document.body.removeChild(root);
  });

  it("presents fullscreen as a labelled modal dialog and closes it on dialog close", async () => {
    const app = createApp({
      render() {
        return h(MermaidViewer, { code: "graph TD; A-->B", title: "My Flow" });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const dialog = root.querySelector("dialog") as HTMLDialogElement;
    expect(dialog).not.toBeNull();
    expect(dialog.open).toBe(false);
    expect(dialog.getAttribute("aria-label")).toBe("My Flow");

    const fsBtn = root.querySelector(
      'button[title="Fullscreen Preview"]',
    ) as HTMLButtonElement | null;
    expect(fsBtn).not.toBeNull();
    fsBtn!.click();
    await nextTick();
    await nextTick();
    expect(dialog.open).toBe(true);
    expect(dialog.textContent).toContain("My Flow");

    // Zoom in so a zoom badge appears.
    const zoomIn = dialog.querySelector(
      'button[title*="Zoom In" i], button[aria-label*="Zoom In" i]',
    ) as HTMLButtonElement | null;
    expect(zoomIn).not.toBeNull();
    zoomIn!.click();
    await nextTick();
    expect(dialog.textContent).toMatch(/\d+%/);

    // Esc: the browser fires `cancel` then closes the dialog.
    dialog.dispatchEvent(new Event("cancel", { cancelable: true }));
    dialog.close();
    await nextTick();
    await nextTick();
    expect(dialog.querySelector(".fixed")).toBeNull();

    // Reopen: zoom must have been reset.
    fsBtn!.click();
    await nextTick();
    await nextTick();
    expect(dialog.open).toBe(true);
    expect(dialog.textContent).not.toMatch(/\d+%/);
    app.unmount();
  });
});
