// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { useShortcuts, isMac } from "./useShortcuts";
import * as api from "../lib/api";

describe("useShortcuts", () => {
  const originalNavigator = globalThis.navigator;

  beforeEach(() => {
    const { overrides, resetAllShortcuts } = useShortcuts();
    resetAllShortcuts("linux");
    resetAllShortcuts("mac");
    resetAllShortcuts("windows");
    overrides.value = {};
  });

  afterEach(() => {
    Object.defineProperty(globalThis, "navigator", {
      value: originalNavigator,
      configurable: true,
      writable: true,
    });
    vi.restoreAllMocks();
  });

  it("provides 13 computed shortcut strings with modifier keys and isMac boolean", () => {
    const {
      isMac: hookIsMac,
      modKey,
      altKey,
      toggleSidebarShortcut,
      toggleArtifactsShortcut,
      toggleDiffShortcut,
      toggleTerminalShortcut,
      sendShortcut,
      commandPaletteShortcut,
      commandPaletteShiftShortcut,
      commandPaletteF1Shortcut,
      toggleFileViewShortcut,
      findShortcut,
      newChatShortcut,
    } = useShortcuts();

    expect(typeof hookIsMac).toBe("boolean");
    expect(typeof isMac).toBe("boolean");

    const expectedMod = modKey.value;
    const expectedAlt = altKey.value;

    expect(toggleSidebarShortcut.value).toBe(`${expectedMod}+B`);
    expect(toggleArtifactsShortcut.value).toBe(`${expectedMod}+${expectedAlt}+B`);
    expect(toggleDiffShortcut.value).toBe(`${expectedMod}+${expectedAlt}+D`);
    expect(toggleTerminalShortcut.value).toBe(`${expectedMod}+\``);
    expect(sendShortcut.value).toBe(`${expectedMod}+Enter`);
    expect(commandPaletteShortcut.value).toBe(`${expectedMod}+P`);
    expect(commandPaletteShiftShortcut.value).toBe(`${expectedMod}+Shift+P`);
    expect(commandPaletteF1Shortcut.value).toBe("F1");
    expect(toggleFileViewShortcut.value).toBe(`${expectedMod}+${expectedAlt}+F`);
    expect(findShortcut.value).toBe(`${expectedMod}+F`);
    expect(newChatShortcut.value).toBe(`${expectedMod}+${expectedAlt}+N`);
  });

  it("dynamically updates computed shortcuts when updateShortcut is called", () => {
    const { toggleSidebarShortcut, newChatShortcut, updateShortcut, currentOS } = useShortcuts();

    expect(toggleSidebarShortcut.value).toBe("Ctrl+B");
    updateShortcut("toggle_sidebar", "Ctrl+Shift+B", currentOS.value);
    expect(toggleSidebarShortcut.value).toBe("Ctrl+Shift+B");

    updateShortcut("new_chat", [], currentOS.value);
    expect(newChatShortcut.value).toBe("Unassigned");
  });

  it("matches shortcuts via matchShortcut method", () => {
    const { matchShortcut } = useShortcuts();

    const sidebarEvent = new KeyboardEvent("keydown", {
      ctrlKey: true,
      code: "KeyB",
      key: "b",
    });
    expect(matchShortcut(sidebarEvent, "toggle_sidebar")).toBe(true);

    const wrongEvent = new KeyboardEvent("keydown", {
      ctrlKey: true,
      altKey: true,
      code: "KeyB",
      key: "b",
    });
    expect(matchShortcut(wrongEvent, "toggle_sidebar")).toBe(false);
  });

  it("loads keybindings from API and sets hasLoadError on failure", async () => {
    const { loadCustomKeybindings, overrides, hasLoadError } = useShortcuts();

    vi.spyOn(api, "getKeybindings").mockResolvedValueOnce({
      overrides: {
        linux: { toggle_sidebar: "Ctrl+Alt+S" },
      },
      exists: true,
    });

    const success = await loadCustomKeybindings();
    expect(success).toBe(true);
    expect(hasLoadError.value).toBe(false);
    expect(overrides.value.linux?.toggle_sidebar).toBe("Ctrl+Alt+S");

    // Test error case
    vi.spyOn(api, "getKeybindings").mockResolvedValueOnce({
      overrides: {},
      error: "Corrupted yaml",
    });

    const failSuccess = await loadCustomKeybindings();
    expect(failSuccess).toBe(false);
    expect(hasLoadError.value).toBe(true);
  });

  it("saves delta keybindings via saveCustomKeybindings", async () => {
    const { updateShortcut, saveCustomKeybindings, currentOS } = useShortcuts();

    updateShortcut("toggle_sidebar", "Ctrl+Shift+B", currentOS.value);
    const saveSpy = vi.spyOn(api, "saveKeybindings").mockResolvedValue({ success: true });

    const result = await saveCustomKeybindings();
    expect(result.success).toBe(true);
    expect(saveSpy).toHaveBeenCalledWith({
      linux: {
        toggle_sidebar: "Ctrl+Shift+B",
      },
    });
  });
});
