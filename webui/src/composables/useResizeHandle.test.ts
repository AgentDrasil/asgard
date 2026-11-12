// @vitest-environment jsdom
import { describe, it, expect, afterEach, vi } from "vitest";
import { createApp, type App, defineComponent, h, nextTick, ref, type Ref } from "vue";
import { useResizeHandle } from "./useResizeHandle";

const apps: App[] = [];

function setup(
  opts: { axis?: "x" | "y"; direction?: 1 | -1; disabled?: Ref<boolean>; max?: Ref<number> } = {},
) {
  const size = ref(300);
  const onCommit = vi.fn<(n: number) => void>();
  let api!: ReturnType<typeof useResizeHandle>;
  const Comp = defineComponent({
    setup() {
      api = useResizeHandle({
        size,
        min: () => 200,
        max: () => opts.max?.value ?? 500,
        axis: opts.axis ?? "x",
        direction: opts.direction ?? 1,
        label: () => "Resize",
        onCommit,
        disabled: () => opts.disabled?.value ?? false,
      });
      return () => h("div", { class: "handle", ...api.handleProps.value });
    },
  });
  const root = document.createElement("div");
  document.body.appendChild(root);
  const app = createApp(Comp);
  apps.push(app);
  app.mount(root);
  const node = root.querySelector(".handle") as HTMLElement;
  const el = {
    element: node,
    attributes: (n: string) => node.getAttribute(n),
    trigger: async (type: string, init: KeyboardEventInit = {}) => {
      node.dispatchEvent(new KeyboardEvent(type, { bubbles: true, cancelable: true, ...init }));
      await nextTick();
    },
  };
  return { wrapper: app, size, onCommit, api: () => api, el };
}

const node = (el: { element: HTMLElement }) => el.element;

const ptr = (type: string, init: Record<string, unknown> = {}) => {
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, ...init });
  Object.defineProperty(e, "pointerId", { value: init.pointerId ?? 1 });
  Object.defineProperty(e, "pointerType", { value: init.pointerType ?? "mouse" });
  return e;
};

afterEach(() => {
  for (const a of apps.splice(0)) a.unmount();
  document.body.style.userSelect = "";
  document.body.style.cursor = "";
  document.body.innerHTML = "";
});

describe("useResizeHandle", () => {
  it("exposes separator ARIA attributes", () => {
    const { el } = setup();
    expect(el.attributes("role")).toBe("separator");
    expect(el.attributes("aria-orientation")).toBe("vertical");
    expect(el.attributes("aria-valuenow")).toBe("300");
    expect(el.attributes("aria-valuemin")).toBe("200");
    expect(el.attributes("aria-valuemax")).toBe("500");
    expect(el.attributes("aria-label")).toBe("Resize");
    expect(el.attributes("tabindex")).toBe("0");
    expect(node(el).style.touchAction).toBe("none");
  });

  it("drags with clamping and commits on pointerup", () => {
    const { el, size, onCommit, api } = setup();
    const node = el.element;
    node.dispatchEvent(ptr("pointerdown", { clientX: 100 }));
    expect(api().isResizing.value).toBe(true);
    expect(document.body.style.userSelect).toBe("none");
    expect(document.body.style.cursor).toBe("col-resize");
    node.dispatchEvent(ptr("pointermove", { clientX: 150 }));
    expect(size.value).toBe(350);
    node.dispatchEvent(ptr("pointermove", { clientX: 900 }));
    expect(size.value).toBe(500);
    node.dispatchEvent(ptr("pointermove", { clientX: -900 }));
    expect(size.value).toBe(200);
    node.dispatchEvent(ptr("pointerup"));
    expect(api().isResizing.value).toBe(false);
    expect(onCommit).toHaveBeenCalledWith(200);
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
  });

  it("inverts direction and uses row-resize on the y axis", () => {
    const { el, size } = setup({ axis: "y", direction: -1 });
    el.element.dispatchEvent(ptr("pointerdown", { clientY: 400 }));
    expect(document.body.style.cursor).toBe("row-resize");
    el.element.dispatchEvent(ptr("pointermove", { clientY: 350 }));
    expect(size.value).toBe(350);
  });

  it("ignores other pointers and reverts without commit on pointercancel", () => {
    const { el, size, api, onCommit } = setup();
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0, pointerId: 1 }));
    el.element.dispatchEvent(ptr("pointermove", { clientX: 50, pointerId: 2 }));
    expect(size.value).toBe(300);
    el.element.dispatchEvent(ptr("pointermove", { clientX: 50, pointerId: 1 }));
    expect(size.value).toBe(350);
    el.element.dispatchEvent(ptr("pointercancel", { pointerId: 1 }));
    expect(size.value).toBe(300);
    expect(onCommit).not.toHaveBeenCalled();
    expect(api().isResizing.value).toBe(false);
    expect(document.body.style.userSelect).toBe("");
  });

  it("abort restores a clamped size when max shrank during the drag", () => {
    const max = ref(500);
    const { el, size, onCommit } = setup({ max });
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0 }));
    el.element.dispatchEvent(ptr("pointermove", { clientX: 100 }));
    expect(size.value).toBe(400);
    max.value = 250;
    el.element.dispatchEvent(ptr("pointercancel"));
    expect(size.value).toBe(250);
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("ignores non-primary mouse buttons", () => {
    const { el, api } = setup();
    el.element.dispatchEvent(ptr("pointerdown", { button: 2 }));
    expect(api().isResizing.value).toBe(false);
  });

  it("resets body styles when unmounted mid-drag", () => {
    const { el, wrapper } = setup();
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0 }));
    expect(document.body.style.userSelect).toBe("none");
    wrapper.unmount();
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
  });

  it("resizes with arrow keys, Shift, Home and End", async () => {
    const { el, size, onCommit } = setup();
    await el.trigger("keydown", { key: "ArrowRight" });
    expect(size.value).toBe(316);
    await el.trigger("keydown", { key: "ArrowLeft", shiftKey: true });
    expect(size.value).toBe(252);
    await el.trigger("keydown", { key: "End" });
    expect(size.value).toBe(500);
    await el.trigger("keydown", { key: "Home" });
    expect(size.value).toBe(200);
    await el.trigger("keydown", { key: "ArrowLeft" });
    expect(size.value).toBe(200);
    expect(onCommit).toHaveBeenLastCalledWith(200);
    expect(el.attributes("aria-valuenow")).toBe("200");
  });

  it("maps keys to grow direction for right-anchored and vertical handles", async () => {
    const left = setup({ direction: -1 });
    await left.el.trigger("keydown", { key: "ArrowLeft" });
    expect(left.size.value).toBe(316);
    const up = setup({ axis: "y", direction: -1 });
    await up.el.trigger("keydown", { key: "ArrowUp" });
    expect(up.size.value).toBe(316);
    await up.el.trigger("keydown", { key: "ArrowDown" });
    expect(up.size.value).toBe(300);
  });

  it("blocks pointerdown and keydown when disabled", async () => {
    const disabled = ref(true);
    const { el, size, onCommit, api } = setup({ disabled });
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0 }));
    expect(api().isResizing.value).toBe(false);
    expect(document.body.style.userSelect).toBe("");
    await el.trigger("keydown", { key: "ArrowRight" });
    expect(size.value).toBe(300);
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("aborts a drag when disabled mid-drag and releases body styles", async () => {
    const disabled = ref(false);
    const { el, size, onCommit, api } = setup({ disabled });
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0 }));
    el.element.dispatchEvent(ptr("pointermove", { clientX: 50 }));
    expect(size.value).toBe(350);
    disabled.value = true;
    await nextTick();
    expect(api().isResizing.value).toBe(false);
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
    el.element.dispatchEvent(ptr("pointermove", { clientX: 100 }));
    expect(size.value).toBe(300);
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("ends and commits on lostpointercapture", () => {
    const { el, size, onCommit, api } = setup();
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0 }));
    el.element.dispatchEvent(ptr("pointermove", { clientX: 40 }));
    el.element.dispatchEvent(ptr("lostpointercapture"));
    expect(api().isResizing.value).toBe(false);
    expect(size.value).toBe(340);
    expect(onCommit).toHaveBeenCalledWith(340);
  });

  it("does not commit when a keyboard resize leaves the size unchanged", async () => {
    const { el, size, onCommit } = setup();
    await el.trigger("keydown", { key: "Home" });
    expect(size.value).toBe(200);
    expect(onCommit).toHaveBeenCalledTimes(1);
    await el.trigger("keydown", { key: "Home" });
    await el.trigger("keydown", { key: "ArrowLeft" });
    expect(onCommit).toHaveBeenCalledTimes(1);
  });

  it("ignores arrow keys with Ctrl, Meta or Alt", async () => {
    const { el, size, onCommit } = setup();
    for (const mod of ["ctrlKey", "metaKey", "altKey"]) {
      await el.trigger("keydown", { key: "ArrowRight", [mod]: true });
    }
    expect(size.value).toBe(300);
    expect(onCommit).not.toHaveBeenCalled();
  });

  it.each(["touch", "pen"])("drags with %s pointers", (pointerType) => {
    const { el, size, onCommit } = setup();
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 10, pointerType, button: -1 }));
    el.element.dispatchEvent(ptr("pointermove", { clientX: 60, pointerType }));
    expect(size.value).toBe(350);
    el.element.dispatchEvent(ptr("pointerup", { pointerType }));
    expect(onCommit).toHaveBeenCalledWith(350);
  });

  it("updates aria-valuenow during a drag", async () => {
    const { el } = setup();
    el.element.dispatchEvent(ptr("pointerdown", { clientX: 0 }));
    el.element.dispatchEvent(ptr("pointermove", { clientX: 75 }));
    await nextTick();
    expect(el.attributes("aria-valuenow")).toBe("375");
  });
});
