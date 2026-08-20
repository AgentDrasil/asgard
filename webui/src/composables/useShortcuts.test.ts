import { describe, it, expect } from "vitest";
import { useShortcuts } from "./useShortcuts";

describe("useShortcuts", () => {
  it("provides shortcut strings with modifier keys", () => {
    const {
      modKey,
      altKey,
      toggleSidebarShortcut,
      toggleArtifactsShortcut,
      toggleDiffShortcut,
      toggleTerminalShortcut,
      sendShortcut,
      fileSearchShortcut,
      commandPaletteShortcut,
      commandPaletteF1Shortcut,
      toggleFileViewShortcut,
    } = useShortcuts();

    expect(toggleSidebarShortcut.value).toBe(`${modKey.value}+B`);
    expect(toggleArtifactsShortcut.value).toBe(`${modKey.value}+${altKey.value}+B`);
    expect(toggleDiffShortcut.value).toBe(`${modKey.value}+${altKey.value}+D`);
    expect(toggleTerminalShortcut.value).toBe(`${modKey.value}+\``);
    expect(sendShortcut.value).toBe(`${modKey.value}+Enter`);
    expect(fileSearchShortcut.value).toBe(`${modKey.value}+P`);
    expect(commandPaletteShortcut.value).toBe(`${modKey.value}+Shift+P`);
    expect(commandPaletteF1Shortcut.value).toBe("F1");
    expect(toggleFileViewShortcut.value).toBe(`${modKey.value}+${altKey.value}+F`);
  });
});
