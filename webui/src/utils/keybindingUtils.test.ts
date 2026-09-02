// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import {
  DEFAULT_KEYBINDING_ACTIONS,
  getDefaultBindingsForOS,
  normalizeShortcut,
  isKeyboardEventMatch,
  formatShortcutDisplay,
  detectConflicts,
  calculateDelta,
  resolveGlobalAction,
  getActionTitleKey,
  getActionDescriptionKey,
} from "./keybindingUtils";

describe("keybindingUtils", () => {
  describe("DEFAULT_KEYBINDING_ACTIONS", () => {
    it("defines 10 default actions with standard ASCII keys", () => {
      expect(DEFAULT_KEYBINDING_ACTIONS).toHaveLength(10);
      const ids = DEFAULT_KEYBINDING_ACTIONS.map((a) => a.id);
      expect(ids).toContain("toggle_sidebar");
      expect(ids).toContain("toggle_artifacts");
      expect(ids).toContain("toggle_diff");
      expect(ids).toContain("toggle_file_view");
      expect(ids).toContain("toggle_terminal");
      expect(ids).toContain("search_files");
      expect(ids).toContain("command_palette");
      expect(ids).toContain("find");
      expect(ids).toContain("send_message");
      expect(ids).toContain("new_chat");
    });

    it("getDefaultBindingsForOS returns normalized array values", () => {
      const linuxBindings = getDefaultBindingsForOS("linux");
      expect(linuxBindings["search_files"]).toEqual(["Ctrl+P"]);
      expect(linuxBindings["command_palette"]).toEqual(["Ctrl+Shift+P", "F1"]);
      expect(linuxBindings["toggle_terminal"]).toEqual(["Ctrl+Backquote"]);
      expect(linuxBindings["toggle_sidebar"]).toEqual(["Ctrl+B"]);

      const macBindings = getDefaultBindingsForOS("mac");
      expect(macBindings["search_files"]).toEqual(["Cmd+P"]);
      expect(macBindings["command_palette"]).toEqual(["Cmd+Shift+P", "F1"]);
      expect(macBindings["toggle_terminal"]).toEqual(["Cmd+Backquote"]);
    });
  });

  describe("normalizeShortcut", () => {
    it("normalizes modifiers and base keys correctly", () => {
      expect(normalizeShortcut("ctrl + b")).toBe("Ctrl+B");
      expect(normalizeShortcut("cmd+alt+d")).toBe("Cmd+Alt+D");
      expect(normalizeShortcut("⌘+⌥+f")).toBe("Cmd+Alt+F");
      expect(normalizeShortcut("control+shift+p")).toBe("Ctrl+Shift+P");
      expect(normalizeShortcut("ctrl+`")).toBe("Ctrl+Backquote");
      expect(normalizeShortcut("f1")).toBe("F1");
      expect(normalizeShortcut("ctrl+enter")).toBe("Ctrl+Enter");
      expect(normalizeShortcut("")).toBe("");
    });
  });

  describe("isKeyboardEventMatch", () => {
    it("matches single key and multi-modifier shortcuts on linux", () => {
      const event = new KeyboardEvent("keydown", {
        ctrlKey: true,
        code: "KeyB",
        key: "b",
      });
      expect(isKeyboardEventMatch(event, "Ctrl+B", "linux")).toBe(true);
      expect(isKeyboardEventMatch(event, "Cmd+B", "linux")).toBe(false);
    });

    it("matches command palette multi-bindings on linux/mac", () => {
      const bindings = ["Ctrl+Shift+P", "F1"];
      const f1Event = new KeyboardEvent("keydown", {
        code: "F1",
        key: "F1",
      });
      expect(isKeyboardEventMatch(f1Event, bindings, "linux")).toBe(true);

      const ctrlShiftPEvent = new KeyboardEvent("keydown", {
        ctrlKey: true,
        shiftKey: true,
        code: "KeyP",
        key: "P",
      });
      expect(isKeyboardEventMatch(ctrlShiftPEvent, bindings, "linux")).toBe(true);
    });

    it("handles Mac Cmd vs Ctrl correctly", () => {
      const macEvent = new KeyboardEvent("keydown", {
        metaKey: true,
        code: "KeyB",
        key: "b",
      });
      expect(isKeyboardEventMatch(macEvent, "Cmd+B", "mac")).toBe(true);
      expect(isKeyboardEventMatch(macEvent, "Ctrl+B", "mac")).toBe(false);
    });

    it("returns false for empty array / empty string (Unassigned semantics)", () => {
      const event = new KeyboardEvent("keydown", {
        ctrlKey: true,
        code: "KeyB",
      });
      expect(isKeyboardEventMatch(event, [], "linux")).toBe(false);
      expect(isKeyboardEventMatch(event, "", "linux")).toBe(false);
    });

    it("strictly rejects extra modifiers (M-3 negative assertions)", () => {
      // Ctrl+Alt+P should NOT trigger command_palette (["Ctrl+Shift+P", "F1"])
      const ctrlAltP = new KeyboardEvent("keydown", {
        ctrlKey: true,
        altKey: true,
        code: "KeyP",
        key: "p",
      });
      expect(isKeyboardEventMatch(ctrlAltP, ["Ctrl+Shift+P", "F1"], "linux")).toBe(false);

      // Shift+F1 should NOT trigger command_palette
      const shiftF1 = new KeyboardEvent("keydown", {
        shiftKey: true,
        code: "F1",
        key: "F1",
      });
      expect(isKeyboardEventMatch(shiftF1, ["Ctrl+Shift+P", "F1"], "linux")).toBe(false);

      // Ctrl+Shift+` should NOT trigger toggle_terminal (Ctrl+Backquote)
      const ctrlShiftBackquote = new KeyboardEvent("keydown", {
        ctrlKey: true,
        shiftKey: true,
        code: "Backquote",
        key: "`",
      });
      expect(isKeyboardEventMatch(ctrlShiftBackquote, "Ctrl+Backquote", "linux")).toBe(false);
    });
  });

  describe("formatShortcutDisplay", () => {
    it("formats shortcuts correctly with OS symbols", () => {
      expect(formatShortcutDisplay("Cmd+Alt+B", "mac")).toBe("⌘+⌥+B");
      expect(formatShortcutDisplay("Cmd+Alt+B", "linux")).toBe("Cmd+Alt+B");
      expect(formatShortcutDisplay("Ctrl+Backquote", "linux")).toBe("Ctrl+`");
      expect(formatShortcutDisplay(["Ctrl+Shift+P", "F1"], "linux")).toBe("Ctrl+Shift+P / F1");
      expect(formatShortcutDisplay([], "linux")).toBe("Unassigned");
      expect(formatShortcutDisplay("", "linux")).toBe("Unassigned");
    });
  });

  describe("detectConflicts", () => {
    it("detects conflicts across actions and ignores empty/unassigned", () => {
      const bindings = {
        action1: "Ctrl+B",
        action2: "Ctrl+B",
        action3: "Ctrl+C",
        action4: [],
        action5: "",
      };
      const conflicts = detectConflicts(bindings);
      expect(conflicts.has("Ctrl+B")).toBe(true);
      expect(conflicts.get("Ctrl+B")).toEqual(["action1", "action2"]);
      expect(conflicts.size).toBe(1);
    });

    it("handles multi-key arrays in conflict detection", () => {
      const bindings = {
        action1: ["Ctrl+P", "F1"],
        action2: "F1",
      };
      const conflicts = detectConflicts(bindings);
      expect(conflicts.has("F1")).toBe(true);
      expect(conflicts.get("F1")).toEqual(["action1", "action2"]);
    });
  });

  describe("calculateDelta", () => {
    it("extracts delta differences including unassigned []", () => {
      const defaults = {
        toggle_sidebar: "Ctrl+B",
        toggle_diff: "Ctrl+Alt+D",
        new_chat: "Ctrl+Alt+N",
      };
      const current = {
        toggle_sidebar: "Ctrl+B", // unchanged
        toggle_diff: "Ctrl+Shift+D", // modified
        new_chat: [], // unassigned
      };

      const delta = calculateDelta(current, defaults);
      expect(delta).toEqual({
        toggle_diff: "Ctrl+Shift+D",
        new_chat: [],
      });
    });
  });

  describe("resolveGlobalAction", () => {
    const defaultLinuxBindings = getDefaultBindingsForOS("linux");

    it("triggers pre-guard search_files even when input is focused", () => {
      const event = new KeyboardEvent("keydown", {
        ctrlKey: true,
        code: "KeyP",
        key: "p",
        cancelable: true,
      });
      const preventSpy = vi.spyOn(event, "preventDefault");
      const stopSpy = vi.spyOn(event, "stopPropagation");

      const action = resolveGlobalAction(event, defaultLinuxBindings, true, false, "linux");
      expect(action).toBe("search_files");
      expect(preventSpy).toHaveBeenCalled();
      expect(stopSpy).toHaveBeenCalled();
    });

    it("triggers pre-guard toggle_terminal with open/close semantic", () => {
      const event = new KeyboardEvent("keydown", {
        ctrlKey: true,
        code: "Backquote",
        key: "`",
        cancelable: true,
      });

      // Terminal is closed -> returns open_terminal_session
      expect(resolveGlobalAction(event, defaultLinuxBindings, true, false, "linux")).toBe(
        "open_terminal_session",
      );

      // Terminal is open -> returns close_terminal
      expect(resolveGlobalAction(event, defaultLinuxBindings, true, true, "linux")).toBe(
        "close_terminal",
      );
    });

    it("triggers command_palette with F1 and Ctrl+Shift+P", () => {
      const f1Event = new KeyboardEvent("keydown", {
        code: "F1",
        key: "F1",
      });
      expect(resolveGlobalAction(f1Event, defaultLinuxBindings, false, false, "linux")).toBe(
        "command_palette",
      );

      const ctrlShiftPEvent = new KeyboardEvent("keydown", {
        ctrlKey: true,
        shiftKey: true,
        code: "KeyP",
        key: "P",
      });
      expect(resolveGlobalAction(ctrlShiftPEvent, defaultLinuxBindings, true, false, "linux")).toBe(
        "command_palette",
      );
    });

    it("suppresses post-guard actions when input is focused", () => {
      const event = new KeyboardEvent("keydown", {
        ctrlKey: true,
        code: "KeyB",
        key: "b",
      });

      // Input not focused -> returns toggle_sidebar
      expect(resolveGlobalAction(event, defaultLinuxBindings, false, false, "linux")).toBe(
        "toggle_sidebar",
      );

      // Input focused -> returns null
      expect(resolveGlobalAction(event, defaultLinuxBindings, true, false, "linux")).toBeNull();
    });

    it("handles new_chat post-guard action", () => {
      const event = new KeyboardEvent("keydown", {
        ctrlKey: true,
        altKey: true,
        code: "KeyN",
        key: "n",
      });

      expect(resolveGlobalAction(event, defaultLinuxBindings, false, false, "linux")).toBe(
        "new_chat",
      );
      expect(resolveGlobalAction(event, defaultLinuxBindings, true, false, "linux")).toBeNull();
    });

    it("resolves according to customized activeBindings overrides", () => {
      const customBindings = {
        ...defaultLinuxBindings,
        toggle_sidebar: ["Ctrl+Alt+S"],
      };

      const oldKey = new KeyboardEvent("keydown", {
        ctrlKey: true,
        code: "KeyB",
        key: "b",
      });
      expect(resolveGlobalAction(oldKey, customBindings, false, false, "linux")).toBeNull();

      const newKey = new KeyboardEvent("keydown", {
        ctrlKey: true,
        altKey: true,
        code: "KeyS",
        key: "s",
      });
      expect(resolveGlobalAction(newKey, customBindings, false, false, "linux")).toBe(
        "toggle_sidebar",
      );
    });
  });

  describe("getActionTitleKey & getActionDescriptionKey", () => {
    it("returns correct i18n translation keys for action IDs", () => {
      expect(getActionTitleKey("toggle_sidebar")).toBe("keybindings.actions.toggle_sidebar.title");
      expect(getActionDescriptionKey("toggle_sidebar")).toBe(
        "keybindings.actions.toggle_sidebar.description",
      );
      expect(getActionTitleKey("search_files")).toBe("keybindings.actions.search_files.title");
      expect(getActionDescriptionKey("search_files")).toBe(
        "keybindings.actions.search_files.description",
      );
    });
  });
});
