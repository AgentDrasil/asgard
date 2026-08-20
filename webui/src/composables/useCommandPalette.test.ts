import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";
import { useCommandPalette } from "./useCommandPalette";
import type { CommandItem } from "../types";

describe("useCommandPalette", () => {
  const mockAction1 = vi.fn<() => void>();
  const mockAction2 = vi.fn<() => void>();
  const mockAction3 = vi.fn<() => void>();

  const commandsList: CommandItem[] = [
    { id: "cmd-1", title: "Open Settings", category: "General", action: mockAction1 },
    { id: "cmd-2", title: "Toggle Terminal", category: "View", action: mockAction2 },
    { id: "cmd-3", title: "Git Commit", category: "Git", action: mockAction3 },
  ];

  it("initializes with empty query, index 0, and all commands", () => {
    const { query, selectedIndex, filteredCommands } = useCommandPalette(commandsList);
    expect(query.value).toBe("");
    expect(selectedIndex.value).toBe(0);
    expect(filteredCommands.value).toEqual(commandsList);
  });

  it("updates filteredCommands reactively when query changes and resets selectedIndex", () => {
    const { query, selectedIndex, filteredCommands, navigateNext } =
      useCommandPalette(commandsList);

    navigateNext();
    expect(selectedIndex.value).toBe(1);

    query.value = "Terminal";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("cmd-2");
    expect(selectedIndex.value).toBe(0);
  });

  it("works with reactive Ref commands source", () => {
    const sourceRef = ref<CommandItem[]>([...commandsList]);
    const { filteredCommands } = useCommandPalette(sourceRef);
    expect(filteredCommands.value).toHaveLength(3);

    sourceRef.value = [{ id: "cmd-new", title: "New Action", action: () => {} }];
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("cmd-new");
  });

  it("clamps selectedIndex when commands source shrinks", () => {
    const sourceRef = ref<CommandItem[]>([...commandsList]);
    const { selectedIndex, navigateNext } = useCommandPalette(sourceRef);

    navigateNext();
    navigateNext();
    expect(selectedIndex.value).toBe(2);

    sourceRef.value = [{ id: "cmd-1", title: "Single remaining", action: () => {} }];
    expect(selectedIndex.value).toBe(0);
  });

  it("cycles navigation forward and backward correctly", () => {
    const { selectedIndex, navigateNext, navigatePrevious } = useCommandPalette(commandsList);

    expect(selectedIndex.value).toBe(0);
    navigateNext();
    expect(selectedIndex.value).toBe(1);
    navigateNext();
    expect(selectedIndex.value).toBe(2);
    // Cycle back to 0
    navigateNext();
    expect(selectedIndex.value).toBe(0);

    // Cycle backward from 0 to last item
    navigatePrevious();
    expect(selectedIndex.value).toBe(2);
    navigatePrevious();
    expect(selectedIndex.value).toBe(1);
  });

  it("handles navigation and selection safely on empty results without NaN or errors", () => {
    const { query, selectedIndex, navigateNext, navigatePrevious, selectCurrent } =
      useCommandPalette(commandsList);

    query.value = "nonexistent_query_xyz";
    expect(selectedIndex.value).toBe(0);

    navigateNext();
    expect(selectedIndex.value).toBe(0);
    expect(Number.isNaN(selectedIndex.value)).toBe(false);

    navigatePrevious();
    expect(selectedIndex.value).toBe(0);
    expect(Number.isNaN(selectedIndex.value)).toBe(false);

    expect(selectCurrent()).toBeNull();
  });

  it("selectCurrent returns the currently active command item", () => {
    const { navigateNext, selectCurrent } = useCommandPalette(commandsList);

    expect(selectCurrent()?.id).toBe("cmd-1");
    navigateNext();
    expect(selectCurrent()?.id).toBe("cmd-2");
  });

  it("reset restores query to empty and selectedIndex to 0", () => {
    const { query, selectedIndex, navigateNext, reset } = useCommandPalette(commandsList);

    query.value = "Git";
    navigateNext();
    reset();

    expect(query.value).toBe("");
    expect(selectedIndex.value).toBe(0);
  });
});
