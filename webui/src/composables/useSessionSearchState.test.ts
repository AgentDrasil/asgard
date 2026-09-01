import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { nextTick } from "vue";
import { useSessionSearchState } from "./useSessionSearchState";
import * as api from "../lib/api";
import type { ChatSession } from "../types";

describe("useSessionSearchState", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("initializes with default empty values", () => {
    const { query, results, selectedIndex, isLoading, errorMessage } = useSessionSearchState();

    expect(query.value).toBe("");
    expect(results.value).toEqual([]);
    expect(selectedIndex.value).toBe(0);
    expect(isLoading.value).toBe(false);
    expect(errorMessage.value).toBe("");
  });

  it("navigates next and previous with index wrapping", () => {
    const { results, selectedIndex, navigateNext, navigatePrevious, selectCurrent } =
      useSessionSearchState();

    // Empty list
    navigateNext();
    expect(selectedIndex.value).toBe(0);
    expect(selectCurrent()).toBeNull();

    // Populate results
    const mockSessions: ChatSession[] = [
      {
        chatID: "1",
        title: "Session One",
        currentAgent: "agent-a",
        runDir: "/tmp/1",
        isRunning: false,
      },
      {
        chatID: "2",
        title: "Session Two",
        currentAgent: "agent-b",
        runDir: "/tmp/2",
        isRunning: true,
      },
      {
        chatID: "3",
        title: "Session Three",
        currentAgent: "agent-c",
        runDir: "/tmp/3",
        isRunning: false,
      },
    ];
    results.value = mockSessions;

    expect(selectCurrent()).toEqual(mockSessions[0]);

    navigateNext();
    expect(selectedIndex.value).toBe(1);
    expect(selectCurrent()).toEqual(mockSessions[1]);

    navigateNext();
    expect(selectedIndex.value).toBe(2);

    // Wrap around to 0
    navigateNext();
    expect(selectedIndex.value).toBe(0);

    // Wrap backwards to 2
    navigatePrevious();
    expect(selectedIndex.value).toBe(2);

    navigatePrevious();
    expect(selectedIndex.value).toBe(1);
  });

  it("executes search on query change with debounce", async () => {
    const mockSessions: ChatSession[] = [
      {
        chatID: "101",
        title: "Test Session",
        currentAgent: "asgard",
        runDir: "/workspace",
        isRunning: false,
      },
    ];
    const searchSpy = vi.spyOn(api, "searchSessions").mockResolvedValue(mockSessions);

    const { query, results, isLoading } = useSessionSearchState(150);

    query.value = "test";
    await nextTick();

    // Before debounce
    expect(searchSpy).not.toHaveBeenCalled();

    // Advance timer to trigger debounce
    await vi.advanceTimersByTimeAsync(150);

    expect(searchSpy).toHaveBeenCalledWith("test", expect.any(AbortSignal));
    expect(results.value).toEqual(mockSessions);
    expect(isLoading.value).toBe(false);
  });

  it("clears results immediately if query becomes empty", async () => {
    const searchSpy = vi.spyOn(api, "searchSessions").mockResolvedValue([]);
    const { query, results } = useSessionSearchState();

    results.value = [
      {
        chatID: "1",
        title: "Existing",
        currentAgent: "asgard",
        runDir: "/workspace",
        isRunning: false,
      },
    ];
    query.value = "   ";
    await nextTick();

    expect(results.value).toEqual([]);
    expect(searchSpy).not.toHaveBeenCalled();
  });

  it("aborts previous request when a new search is performed", async () => {
    const signals: AbortSignal[] = [];
    vi.spyOn(api, "searchSessions").mockImplementation((_query, signal) => {
      if (signal) {
        signals.push(signal);
      }
      return new Promise((resolve) => {
        setTimeout(() => {
          resolve([
            {
              chatID: "1",
              title: "Result",
              currentAgent: "asgard",
              runDir: "/tmp",
              isRunning: false,
            },
          ]);
        }, 500);
      });
    });

    const { query } = useSessionSearchState(100);

    query.value = "first";
    await nextTick();
    await vi.advanceTimersByTimeAsync(100);

    expect(signals.length).toBe(1);
    expect(signals[0].aborted).toBe(false);

    // Trigger second search
    query.value = "second";
    await nextTick();
    await vi.advanceTimersByTimeAsync(100);

    expect(signals.length).toBe(2);
    // First signal should have been aborted
    expect(signals[0].aborted).toBe(true);
    expect(signals[1].aborted).toBe(false);
  });

  it("resets state completely", () => {
    const { query, results, selectedIndex, isLoading, errorMessage, reset } =
      useSessionSearchState();

    query.value = "query";
    results.value = [
      { chatID: "1", title: "Session", currentAgent: "asgard", runDir: "/tmp", isRunning: false },
    ];
    selectedIndex.value = 2;
    isLoading.value = true;
    errorMessage.value = "some error";

    reset();

    expect(query.value).toBe("");
    expect(results.value).toEqual([]);
    expect(selectedIndex.value).toBe(0);
    expect(isLoading.value).toBe(false);
    expect(errorMessage.value).toBe("");
  });
});
