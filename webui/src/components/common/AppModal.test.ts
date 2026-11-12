// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { createApp, h, nextTick, ref } from "vue";
import AppModal from "./AppModal.vue";

describe("AppModal.vue", () => {
  it("opens and closes the native dialog following the open prop", async () => {
    const open = ref(false);
    const root = document.createElement("div");
    const app = createApp({
      render: () =>
        h(
          AppModal,
          {
            open: open.value,
            ariaLabel: "Test",
            "onUpdate:open": (v: boolean) => (open.value = v),
          },
          () => h("p", { id: "content" }, "hi"),
        ),
    });
    app.mount(root);
    const dialog = root.querySelector("dialog") as HTMLDialogElement;
    expect(dialog.hasAttribute("open")).toBe(false);
    expect(root.querySelector("#content")).toBeNull();

    open.value = true;
    await nextTick();
    expect(dialog.hasAttribute("open")).toBe(true);
    expect(dialog.getAttribute("aria-label")).toBe("Test");
    expect(root.querySelector("#content")).not.toBeNull();

    // Backdrop click (press and release on the dialog element) closes it.
    dialog.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    dialog.click();
    await nextTick();
    expect(open.value).toBe(false);
    expect(dialog.hasAttribute("open")).toBe(false);

    app.unmount();
  });

  it("does not close when clicking content", async () => {
    const open = ref(true);
    const root = document.createElement("div");
    const app = createApp({
      render: () =>
        h(AppModal, { open: open.value, "onUpdate:open": (v: boolean) => (open.value = v) }, () =>
          h("button", { id: "inner" }, "x"),
        ),
    });
    app.mount(root);
    await nextTick();
    const inner = root.querySelector("#inner") as HTMLButtonElement;
    inner.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    inner.click();
    await nextTick();
    expect(open.value).toBe(true);
    app.unmount();
  });
  function mountModal(props: Record<string, unknown> = {}, initial = true) {
    const open = ref(initial);
    const updates: boolean[] = [];
    const closes: number[] = [];
    const root = document.createElement("div");
    const app = createApp({
      render: () =>
        h(
          AppModal,
          {
            open: open.value,
            ...props,
            "onUpdate:open": (v: boolean) => {
              updates.push(v);
              open.value = v;
            },
            onClose: () => closes.push(1),
          },
          () => h("p", "hi"),
        ),
    });
    app.mount(root);
    const dialog = root.querySelector("dialog") as HTMLDialogElement;
    return { open, updates, closes, dialog, app };
  }

  it("native close emits update:open and close exactly once", async () => {
    const { open, updates, closes, dialog, app } = mountModal();
    await nextTick();
    expect(dialog.open).toBe(true);
    dialog.close();
    await nextTick();
    expect(updates).toEqual([false]);
    expect(closes.length).toBe(1);
    expect(open.value).toBe(false);
    app.unmount();
  });

  it("Esc (cancel then close) emits once and no extra close after open=false", async () => {
    const { updates, closes, dialog, app } = mountModal();
    await nextTick();
    const cancel = new Event("cancel", { cancelable: true });
    dialog.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    dialog.close();
    await nextTick();
    await nextTick();
    expect(updates).toEqual([false]);
    expect(closes.length).toBe(1);
    app.unmount();
  });

  it("parent setting open=false does not emit close", async () => {
    const { open, updates, closes, app } = mountModal();
    await nextTick();
    open.value = false;
    await nextTick();
    await nextTick();
    expect(updates).toEqual([]);
    expect(closes.length).toBe(0);
    app.unmount();
  });

  it("reopens after close by calling showModal again", async () => {
    const { open, dialog, app } = mountModal();
    await nextTick();
    const spy = vi.spyOn(dialog, "showModal");
    dialog.close();
    await nextTick();
    expect(open.value).toBe(false);
    open.value = true;
    await nextTick();
    await nextTick();
    expect(spy).toHaveBeenCalledTimes(1);
    expect(dialog.open).toBe(true);
    app.unmount();
  });

  it("sets aria-labelledby, and aria-label only without labelledby", async () => {
    const a = mountModal({ labelledby: "t", ariaLabel: "Lbl" });
    expect(a.dialog.getAttribute("aria-labelledby")).toBe("t");
    expect(a.dialog.hasAttribute("aria-label")).toBe(false);
    a.app.unmount();
    const b = mountModal({ ariaLabel: "Lbl" });
    expect(b.dialog.getAttribute("aria-label")).toBe("Lbl");
    expect(b.dialog.hasAttribute("aria-labelledby")).toBe(false);
    b.app.unmount();
  });

  it("does not close when pressed in content and released on backdrop", async () => {
    const { open, dialog, app } = mountModal();
    await nextTick();
    const inner = dialog.querySelector("p") as HTMLElement;
    inner.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    dialog.click();
    await nextTick();
    expect(open.value).toBe(true);
    app.unmount();
  });

  it("persistent blocks cancel and backdrop click", async () => {
    const { open, updates, dialog, app } = mountModal({ persistent: true });
    await nextTick();
    const cancel = new Event("cancel", { cancelable: true });
    dialog.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    dialog.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    dialog.click();
    await nextTick();
    expect(open.value).toBe(true);
    expect(dialog.open).toBe(true);
    expect(updates).toEqual([]);
    app.unmount();
  });

  it("persistent re-shows after a native close without cancel", async () => {
    const { open, updates, dialog, app } = mountModal({ persistent: true });
    await nextTick();
    dialog.close();
    await nextTick();
    expect(open.value).toBe(true);
    expect(dialog.open).toBe(true);
    expect(updates).toEqual([]);
    app.unmount();
  });
});
