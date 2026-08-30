import { describe, it, expect } from "vitest";
import { useShortcuts } from "./useShortcuts";

describe("useShortcuts", () => {
  it("provides shortcut strings with modifier keys", () => {
    const {
      toggleSidebarShortcut,
      toggleArtifactsShortcut,
      toggleDiffShortcut,
      toggleTerminalShortcut,
      sendShortcut,
      commandPaletteShortcut,
      commandPaletteShiftShortcut,
      commandPaletteF1Shortcut,
      toggleFileViewShortcut,
    } = useShortcuts();

    const isMac = typeof navigator !== "undefined" && /mac/i.test(navigator.platform);
    const expectedMod = isMac ? "⌘" : "Ctrl";
    const expectedAlt = isMac ? "⌥" : "Alt";

    expect(toggleSidebarShortcut.value).toBe(`${expectedMod}+B`);
    expect(toggleArtifactsShortcut.value).toBe(`${expectedMod}+${expectedAlt}+B`);
    expect(toggleDiffShortcut.value).toBe(`${expectedMod}+${expectedAlt}+D`);
    expect(toggleTerminalShortcut.value).toBe(`${expectedMod}+\``);
    expect(sendShortcut.value).toBe(`${expectedMod}+Enter`);
    expect(commandPaletteShortcut.value).toBe(`${expectedMod}+P`);
    expect(commandPaletteShiftShortcut.value).toBe(`${expectedMod}+Shift+P`);
    expect(commandPaletteF1Shortcut.value).toBe("F1");
    expect(toggleFileViewShortcut.value).toBe(`${expectedMod}+${expectedAlt}+F`);
  });
});
