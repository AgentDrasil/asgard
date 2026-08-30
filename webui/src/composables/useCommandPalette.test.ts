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

  it("filters and executes core commands (Settings, Logs, Config, Reload, Restart, Search Files)", () => {
    const actions = {
      openSettings: vi.fn<() => void>(),
      openLogs: vi.fn<() => void>(),
      editConfig: vi.fn<() => void>(),
      reloadAgents: vi.fn<() => void>(),
      restartServer: vi.fn<() => void>(),
      searchFiles: vi.fn<() => void>(),
    };

    const coreCommands: CommandItem[] = [
      { id: "open-settings", title: "Open Settings", action: actions.openSettings },
      { id: "open-logs", title: "Open System Logs & Diagnostics", action: actions.openLogs },
      { id: "edit-config", title: "Open Config Editor", action: actions.editConfig },
      { id: "reload-agents", title: "Reload Agents", action: actions.reloadAgents },
      { id: "restart-server", title: "Restart Server", action: actions.restartServer },
      { id: "search-files", title: "Search Files in Workspace", action: actions.searchFiles },
    ];

    const { query, filteredCommands, selectCurrent } = useCommandPalette(coreCommands);

    // Test Settings command
    query.value = "settings";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("open-settings");
    selectCurrent()?.action();
    expect(actions.openSettings).toHaveBeenCalledTimes(1);

    // Test Logs command
    query.value = "logs";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("open-logs");
    selectCurrent()?.action();
    expect(actions.openLogs).toHaveBeenCalledTimes(1);

    // Test Config command
    query.value = "config";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("edit-config");
    selectCurrent()?.action();
    expect(actions.editConfig).toHaveBeenCalledTimes(1);

    // Test Reload command
    query.value = "reload";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("reload-agents");
    selectCurrent()?.action();
    expect(actions.reloadAgents).toHaveBeenCalledTimes(1);

    // Test Restart command
    query.value = "restart";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("restart-server");
    selectCurrent()?.action();
    expect(actions.restartServer).toHaveBeenCalledTimes(1);

    // Test Search Files command
    query.value = "search files";
    expect(filteredCommands.value).toHaveLength(1);
    expect(filteredCommands.value[0].id).toBe("search-files");
    selectCurrent()?.action();
    expect(actions.searchFiles).toHaveBeenCalledTimes(1);
  });
});
