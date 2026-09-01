import { ref, watch } from "vue";
import { searchSessions } from "../lib/api";
import type { ChatSession } from "../types";

export function useSessionSearchState(debounceMs = 200) {
  const query = ref("");
  const results = ref<ChatSession[]>([]);
  const selectedIndex = ref(0);
  const isLoading = ref(false);
  const errorMessage = ref("");

  let currentAbortController: AbortController | null = null;
  let debounceTimeout: ReturnType<typeof setTimeout> | null = null;

  const executeSearch = async (searchQuery: string) => {
    if (currentAbortController) {
      currentAbortController.abort();
      currentAbortController = null;
    }

    const trimmed = searchQuery.trim();
    if (!trimmed) {
      results.value = [];
      selectedIndex.value = 0;
      isLoading.value = false;
      errorMessage.value = "";
      return;
    }

    const controller = new AbortController();
    currentAbortController = controller;
    isLoading.value = true;
    errorMessage.value = "";

    try {
      const data = await searchSessions(trimmed, controller.signal);
      if (!controller.signal.aborted) {
        results.value = data;
        selectedIndex.value = 0;
      }
    } catch (err: any) {
      if (err?.name !== "AbortError") {
        errorMessage.value = err?.message || "Search failed";
        results.value = [];
      }
    } finally {
      if (currentAbortController === controller) {
        isLoading.value = false;
        currentAbortController = null;
      }
    }
  };

  watch(query, (newVal) => {
    if (debounceTimeout) {
      clearTimeout(debounceTimeout);
      debounceTimeout = null;
    }
    if (!newVal.trim()) {
      executeSearch("");
      return;
    }
    debounceTimeout = setTimeout(() => {
      executeSearch(newVal);
    }, debounceMs);
  });

  const navigateNext = () => {
    if (results.value.length === 0) {
      selectedIndex.value = 0;
      return;
    }
    selectedIndex.value = (selectedIndex.value + 1) % results.value.length;
  };

  const navigatePrevious = () => {
    if (results.value.length === 0) {
      selectedIndex.value = 0;
      return;
    }
    selectedIndex.value = (selectedIndex.value - 1 + results.value.length) % results.value.length;
  };

  const selectCurrent = (): ChatSession | null => {
    if (results.value.length === 0) return null;
    if (selectedIndex.value < 0 || selectedIndex.value >= results.value.length) {
      return null;
    }
    return results.value[selectedIndex.value] ?? null;
  };

  const reset = () => {
    if (debounceTimeout) {
      clearTimeout(debounceTimeout);
      debounceTimeout = null;
    }
    if (currentAbortController) {
      currentAbortController.abort();
      currentAbortController = null;
    }
    query.value = "";
    results.value = [];
    selectedIndex.value = 0;
    isLoading.value = false;
    errorMessage.value = "";
  };

  return {
    query,
    results,
    selectedIndex,
    isLoading,
    errorMessage,
    executeSearch,
    navigateNext,
    navigatePrevious,
    selectCurrent,
    reset,
  };
}
