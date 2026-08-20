import { ref, computed, watch, unref, type Ref, type ComputedRef } from "vue";
import type { CommandItem } from "../types";
import { filterCommands } from "../utils/fuzzyMatch";

export type CommandPaletteCommandsSource =
  | Ref<CommandItem[]>
  | ComputedRef<CommandItem[]>
  | CommandItem[];

export function useCommandPalette(commandsSource: CommandPaletteCommandsSource) {
  const query = ref("");
  const selectedIndex = ref(0);

  const filteredCommands = computed(() => {
    const list = unref(commandsSource) || [];
    return filterCommands(list, query.value);
  });

  watch(
    query,
    () => {
      selectedIndex.value = 0;
    },
    { flush: "sync" },
  );

  const navigateNext = () => {
    const len = filteredCommands.value.length;
    if (len === 0) {
      selectedIndex.value = 0;
      return;
    }
    selectedIndex.value = (selectedIndex.value + 1) % len;
  };

  const navigatePrevious = () => {
    const len = filteredCommands.value.length;
    if (len === 0) {
      selectedIndex.value = 0;
      return;
    }
    selectedIndex.value = (selectedIndex.value - 1 + len) % len;
  };

  const selectCurrent = (): CommandItem | null => {
    const list = filteredCommands.value;
    if (list.length === 0 || selectedIndex.value < 0 || selectedIndex.value >= list.length) {
      return null;
    }
    return list[selectedIndex.value] ?? null;
  };

  const reset = () => {
    query.value = "";
    selectedIndex.value = 0;
  };

  return {
    query,
    selectedIndex,
    filteredCommands,
    navigateNext,
    navigatePrevious,
    selectCurrent,
    reset,
  };
}
