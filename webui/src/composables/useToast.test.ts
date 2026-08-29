import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useToast } from "./useToast";

describe("useToast composable", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    const { clear } = useToast();
    clear();
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
});
