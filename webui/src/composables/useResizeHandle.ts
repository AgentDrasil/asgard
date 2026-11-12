import { computed, onUnmounted, ref, watch, type Ref } from "vue";

export interface ResizeHandleOptions {
  /** Current size in px (width for axis "x", height for axis "y"). Written on drag/keyboard. */
  size: Ref<number>;
  min: () => number;
  max: () => number;
  /** Drag axis. "x" resizes width, "y" resizes height. */
  axis: "x" | "y";
  /**
   * +1: size grows when the pointer moves right/down (e.g. left sidebar, handle on its right edge).
   * -1: size grows when the pointer moves left/up (e.g. right panel, bottom-anchored terminal).
   */
  direction: 1 | -1;
  /** Accessible name for the separator. */
  label: () => string;
  /** Called once a drag ends or a keyboard resize happens, e.g. to persist the size. */
  onCommit?: (size: number) => void;
  /** When true, the handle ignores input. */
  disabled?: () => boolean;
  /** Keyboard step in px (default 16); Shift multiplies it (bigStep, default 64). */
  step?: number;
  bigStep?: number;
}

/**
 * Pointer-event based resize handle (mouse, touch and pen) with pointer capture,
 * keyboard resizing and separator ARIA semantics. Bind the returned `handleProps`
 * to the handle element with `v-bind`.
 */
export function useResizeHandle(options: ResizeHandleOptions) {
  const { size, axis, direction } = options;
  const step = options.step ?? 16;
  const bigStep = options.bigStep ?? 64;
  const isResizing = ref(false);

  let activeEl: HTMLElement | null = null;
  let activePointerId: number | null = null;
  let startPos = 0;
  let startSize = 0;

  const clamp = (v: number) => {
    const lo = options.min();
    const hi = Math.max(lo, options.max());
    return Math.min(Math.max(v, lo), hi);
  };

  const resetBody = () => {
    if (typeof document === "undefined") return;
    document.body.style.userSelect = "";
    document.body.style.cursor = "";
  };

  const releaseCapture = () => {
    if (activeEl && activePointerId !== null) {
      try {
        if (activeEl.hasPointerCapture?.(activePointerId)) {
          activeEl.releasePointerCapture?.(activePointerId);
        }
      } catch {
        // ignore
      }
    }
    activeEl = null;
    activePointerId = null;
  };

  const finish = (commit: boolean) => {
    const wasResizing = isResizing.value;
    isResizing.value = false;
    releaseCapture();
    resetBody();
    if (wasResizing && commit) options.onCommit?.(size.value);
  };

  /** Abort an in-flight drag: revert to the starting size and do not commit. */
  const abort = () => {
    if (isResizing.value) size.value = clamp(startSize);
    finish(false);
  };

  watch(
    () => options.disabled?.() ?? false,
    (d) => {
      if (d && isResizing.value) abort();
    },
    { flush: "sync" },
  );

  const onPointerdown = (e: PointerEvent) => {
    if (options.disabled?.()) return;
    if (e.pointerType === "mouse" && e.button !== 0) return;
    if (isResizing.value) finish(true);
    e.preventDefault();
    const el = e.currentTarget as HTMLElement | null;
    activeEl = el;
    activePointerId = e.pointerId;
    try {
      el?.setPointerCapture?.(e.pointerId);
    } catch {
      // ignore
    }
    startPos = axis === "x" ? e.clientX : e.clientY;
    startSize = size.value;
    isResizing.value = true;
    document.body.style.userSelect = "none";
    document.body.style.cursor = axis === "x" ? "col-resize" : "row-resize";
  };

  const onPointermove = (e: PointerEvent) => {
    if (!isResizing.value || e.pointerId !== activePointerId) return;
    if (options.disabled?.()) {
      abort();
      return;
    }
    const pos = axis === "x" ? e.clientX : e.clientY;
    size.value = clamp(startSize + direction * (pos - startPos));
  };

  const onPointerup = (e: PointerEvent) => {
    if (e.pointerId !== activePointerId) return;
    finish(true);
  };

  const onPointercancel = (e: PointerEvent) => {
    if (e.pointerId !== activePointerId) return;
    abort();
  };

  const onLostpointercapture = (e: PointerEvent) => {
    if (e.pointerId !== activePointerId) return;
    finish(true);
  };

  const onKeydown = (e: KeyboardEvent) => {
    if (options.disabled?.()) return;
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    const growKey =
      axis === "x"
        ? direction === 1
          ? "ArrowRight"
          : "ArrowLeft"
        : direction === 1
          ? "ArrowDown"
          : "ArrowUp";
    const shrinkKey =
      axis === "x"
        ? direction === 1
          ? "ArrowLeft"
          : "ArrowRight"
        : direction === 1
          ? "ArrowUp"
          : "ArrowDown";
    const delta = e.shiftKey ? bigStep : step;
    let next: number;
    if (e.key === growKey) next = size.value + delta;
    else if (e.key === shrinkKey) next = size.value - delta;
    else if (e.key === "Home") next = options.min();
    else if (e.key === "End") next = options.max();
    else return;
    e.preventDefault();
    const clamped = clamp(next);
    if (clamped !== size.value) {
      size.value = clamped;
      options.onCommit?.(clamped);
    }
  };

  const handleProps = computed(() => ({
    role: "separator" as const,
    tabindex: 0,
    "aria-orientation": axis === "x" ? ("vertical" as const) : ("horizontal" as const),
    "aria-valuenow": Math.round(size.value),
    "aria-valuemin": options.min(),
    "aria-valuemax": Math.max(options.min(), options.max()),
    "aria-label": options.label(),
    style: { touchAction: "none" },
    onPointerdown,
    onPointermove,
    onPointerup,
    onPointercancel,
    onLostpointercapture,
    onKeydown,
  }));

  /** Abort any in-flight drag and restore body styles. Safe to call repeatedly. */
  const cleanup = () => {
    isResizing.value = false;
    releaseCapture();
    resetBody();
  };

  onUnmounted(cleanup);

  return { isResizing, handleProps, cleanup };
}
