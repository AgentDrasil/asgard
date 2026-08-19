import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ref, nextTick } from "vue";
import { useFileSearchState } from "./useFileSearchState";
import * as api from "../lib/api";

describe("useFileSearchState", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("initializes with default empty values", () => {
    const { query, results, selectedIndex, isLoading, errorMessage } = useFileSearchState("sess-1");

    expect(query.value).toBe("");
    expect(results.value).toEqual([]);
    expect(selectedIndex.value).toBe(0);
    expect(isLoading.value).toBe(false);
    expect(errorMessage.value).toBe("");
  });

  it("navigates next and previous with index wrapping", () => {
    const { results, selectedIndex, navigateNext, navigatePrevious, selectCurrent } =
      useFileSearchState("sess-1");

    // Empty list
    navigateNext();
    expect(selectedIndex.value).toBe(0);
    expect(selectCurrent()).toBeNull();

    // Populate results
    results.value = [
      { path: "a.ts", name: "a.ts", ext: "ts", size: 10 },
      { path: "b.ts", name: "b.ts", ext: "ts", size: 20 },
      { path: "c.ts", name: "c.ts", ext: "ts", size: 30 },
    ];

    expect(selectCurrent()).toEqual(results.value[0]);

    navigateNext();
    expect(selectedIndex.value).toBe(1);
    expect(selectCurrent()).toEqual(results.value[1]);

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
    const mockFiles = [{ path: "test.go", name: "test.go", ext: "go", size: 100 }];
    const searchSpy = vi.spyOn(api, "searchFiles").mockResolvedValue(mockFiles);

    const sessionId = ref("session-xyz");
    const { query, results, isLoading } = useFileSearchState(sessionId, 150);

    query.value = "test";
    await nextTick();

    // Before debounce
    expect(searchSpy).not.toHaveBeenCalled();

    // Advance timer to trigger debounce
    await vi.advanceTimersByTimeAsync(150);

    expect(searchSpy).toHaveBeenCalledWith("session-xyz", "test", 50, expect.any(AbortSignal));
    expect(results.value).toEqual(mockFiles);
    expect(isLoading.value).toBe(false);
  });

  it("clears results immediately if query becomes empty", async () => {
    const searchSpy = vi.spyOn(api, "searchFiles").mockResolvedValue([]);
    const { query, results } = useFileSearchState("sess-1");

    results.value = [{ path: "x.ts", name: "x.ts", ext: "ts", size: 10 }];
    query.value = "   ";
    await nextTick();

    expect(results.value).toEqual([]);
    expect(searchSpy).not.toHaveBeenCalled();
  });

  it("resets state completely", () => {
    const { query, results, selectedIndex, isLoading, errorMessage, reset } =
      useFileSearchState("sess-1");

    query.value = "query";
    results.value = [{ path: "a.ts", name: "a.ts", ext: "ts", size: 10 }];
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
