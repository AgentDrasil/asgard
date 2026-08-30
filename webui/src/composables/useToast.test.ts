import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useToast } from "./useToast";

describe("useToast composable", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    const { clear, clearToastHistory } = useToast();
    clear();
    clearToastHistory();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("adds success, info, warning and error toasts to queue", () => {
    const { toasts, info, success, warning, error } = useToast();

    info("Info msg", { title: "Info Title" });
    success("Success msg");
    warning("Warning msg");
    error("Error msg");

    expect(toasts.value).toHaveLength(4);
    expect(toasts.value[0]).toMatchObject({
      type: "info",
      message: "Info msg",
      title: "Info Title",
      duration: 5000,
    });
    expect(toasts.value[1]).toMatchObject({
      type: "success",
      message: "Success msg",
      duration: 5000,
    });
    expect(toasts.value[2]).toMatchObject({
      type: "warning",
      message: "Warning msg",
      duration: 5000,
    });
    expect(toasts.value[3]).toMatchObject({
      type: "error",
      message: "Error msg",
      duration: 0,
    });
  });

  it("removes toast by ID", () => {
    const { toasts, success, removeToast } = useToast();
    const id = success("Task completed");

    expect(toasts.value).toHaveLength(1);
    removeToast(id);
    expect(toasts.value).toHaveLength(0);
  });

  it("automatically removes toast after duration expires", () => {
    const { toasts, info } = useToast();
    info("Auto close message", { duration: 3000 });

    expect(toasts.value).toHaveLength(1);

    vi.advanceTimersByTime(2999);
    expect(toasts.value).toHaveLength(1);

    vi.advanceTimersByTime(1);
    expect(toasts.value).toHaveLength(0);
  });

  it("does not automatically remove error toasts with duration 0", () => {
    const { toasts, error } = useToast();
    error("Fatal error");

    expect(toasts.value).toHaveLength(1);
    vi.advanceTimersByTime(60000);
    expect(toasts.value).toHaveLength(1);
  });

  it("clears all toasts and timers", () => {
    const { toasts, info, warning, clear } = useToast();
    info("1");
    warning("2");
    expect(toasts.value).toHaveLength(2);

    clear();
    expect(toasts.value).toHaveLength(0);
  });

  it("tracks toast history chronologically with timestamps", () => {
    const { toastHistory, info, warning, error, clear } = useToast();
    info("Info event");
    warning("Warning event", { title: "Degraded" });
    error("Error event");

    expect(toastHistory.value).toHaveLength(3);
    expect(toastHistory.value[0].message).toBe("Info event");
    expect(toastHistory.value[0].timestamp).toBeDefined();
    expect(toastHistory.value[1].title).toBe("Degraded");
    expect(toastHistory.value[2].type).toBe("error");

    // Clearing active toasts should not wipe historical logs
    clear();
    expect(toastHistory.value).toHaveLength(3);
  });

  it("records repeated calls into toastHistory even when floating duplicate is filtered", () => {
    const { toasts, toastHistory, warning } = useToast();
    warning("Repeat warning");
    warning("Repeat warning");

    // Floating toasts only show 1 to avoid clutter
    expect(toasts.value).toHaveLength(1);
    // History contains both occurrences
    expect(toastHistory.value).toHaveLength(2);
  });

  it("clears toast history when clearToastHistory is called", () => {
    const { toastHistory, info, clearToastHistory } = useToast();
    info("Msg 1");
    info("Msg 2");
    expect(toastHistory.value).toHaveLength(2);

    clearToastHistory();
    expect(toastHistory.value).toHaveLength(0);
  });

  it("caps toast history to at most 500 items", () => {
    const { toastHistory, info } = useToast();
    for (let i = 0; i < 520; i++) {
      info(`Msg ${i}`);
    }
    expect(toastHistory.value).toHaveLength(500);
    expect(toastHistory.value[0].message).toBe("Msg 20");
    expect(toastHistory.value[499].message).toBe("Msg 519");
  });
});
