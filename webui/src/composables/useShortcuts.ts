import { ref, computed } from "vue";
import type { KeybindingsOverrides, SupportedOS } from "../types";
import { detectClientOS, isMacOS } from "../utils/platform";
import {
  DEFAULT_KEYBINDING_ACTIONS,
  getDefaultBindingsForOS,
  formatShortcutDisplay,
  isKeyboardEventMatch,
  normalizeShortcut,
  calculateDelta,
} from "../utils/keybindingUtils";
import { getKeybindings, saveKeybindings } from "../lib/api";

// Single source of truth for isMac boolean export
export const isMac: boolean = isMacOS();

// Global reactive states
const overrides = ref<KeybindingsOverrides>({});
const hasLoadError = ref(false);
const isLoading = ref(false);

export function useShortcuts() {
  const currentOS = computed<SupportedOS>(() => detectClientOS());

  // Active bindings map for current OS: actionId -> string[]
  const activeBindings = computed<Record<string, string[]>>(() => {
    const os = currentOS.value;
    const defaults = getDefaultBindingsForOS(os);
    const osOverrides = overrides.value[os] || {};

    const merged: Record<string, string[]> = { ...defaults };
    for (const [actionId, rawKeys] of Object.entries(osOverrides)) {
      if (Array.isArray(rawKeys)) {
        merged[actionId] = rawKeys.map(normalizeShortcut).filter(Boolean);
      } else if (typeof rawKeys === "string") {
        const norm = normalizeShortcut(rawKeys);
        merged[actionId] = norm ? [norm] : [];
      } else {
        merged[actionId] = [];
      }
    }
    return merged;
  });

  // Backward compatibility: 13 computed shortcut properties
  const modKey = computed(() => (currentOS.value === "mac" ? "⌘" : "Ctrl"));
  const altKey = computed(() => (currentOS.value === "mac" ? "⌥" : "Alt"));

  const toggleSidebarShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["toggle_sidebar"] || [], currentOS.value),
  );

  const toggleArtifactsShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["toggle_artifacts"] || [], currentOS.value),
  );

  const toggleDiffShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["toggle_diff"] || [], currentOS.value),
  );

  const toggleTerminalShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["toggle_terminal"] || [], currentOS.value),
  );

  const sendShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["send_message"] || [], currentOS.value),
  );

  // Command palette variants:
  // Note: activeBindings["command_palette"] is string[] (e.g. ["Ctrl+P", "Ctrl+Shift+P", "F1"])
  const commandPaletteShortcut = computed(() => {
    const keys = activeBindings.value["command_palette"] || [];
    // If customized or default, format the primary key or entire list
    return keys.length > 0 ? formatShortcutDisplay(keys[0], currentOS.value) : "Unassigned";
  });

  const commandPaletteShiftShortcut = computed(() => {
    const keys = activeBindings.value["command_palette"] || [];
    return keys.length > 1
      ? formatShortcutDisplay(keys[1], currentOS.value)
      : `${modKey.value}+Shift+P`;
  });

  const commandPaletteF1Shortcut = computed(() => "F1");

  const toggleFileViewShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["toggle_file_view"] || [], currentOS.value),
  );

  const findShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["find"] || [], currentOS.value),
  );

  const newChatShortcut = computed(() =>
    formatShortcutDisplay(activeBindings.value["new_chat"] || [], currentOS.value),
  );

  // Helpers
  const matchShortcut = (event: KeyboardEvent, actionId: string): boolean => {
    const keys = activeBindings.value[actionId] || [];
    return isKeyboardEventMatch(event, keys, currentOS.value);
  };

  const updateShortcut = (
    actionId: string,
    keys: string | string[],
    os: SupportedOS = currentOS.value,
  ) => {
    if (!overrides.value[os]) {
      overrides.value[os] = {};
    }
    overrides.value[os]![actionId] = keys;
  };

  const resetShortcut = (actionId: string, os: SupportedOS = currentOS.value) => {
    if (overrides.value[os]) {
      delete overrides.value[os]![actionId];
      if (Object.keys(overrides.value[os]!).length === 0) {
        delete overrides.value[os];
      }
    }
  };

  const resetAllShortcuts = (os: SupportedOS = currentOS.value) => {
    if (overrides.value[os]) {
      delete overrides.value[os];
    }
  };

  const loadCustomKeybindings = async (): Promise<boolean> => {
    isLoading.value = true;
    try {
      const resp = await getKeybindings();
      if (!resp || resp.error) {
        hasLoadError.value = true;
        return false;
      }
      hasLoadError.value = false;
      overrides.value = resp.overrides || {};
      return true;
    } catch {
      hasLoadError.value = true;
      return false;
    } finally {
      isLoading.value = false;
    }
  };

  const saveCustomKeybindings = async (
    targetOS: SupportedOS = currentOS.value,
  ): Promise<{ success: boolean; error?: string }> => {
    const defaults = getDefaultBindingsForOS(targetOS);

    // Compute delta for targetOS
    const osOverrides = overrides.value[targetOS] || {};
    const delta = calculateDelta(osOverrides, defaults);

    const payload: KeybindingsOverrides = {
      [targetOS]: delta,
    };

    const res = await saveKeybindings(payload);
    if (res.success) {
      overrides.value[targetOS] = delta;
    }
    return res;
  };

  return {
    isMac,
    modKey,
    altKey,
    currentOS,
    overrides,
    hasLoadError,
    isLoading,
    activeBindings,
    DEFAULT_KEYBINDING_ACTIONS,
    // 13 computed shortcuts
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
    // methods
    matchShortcut,
    updateShortcut,
    resetShortcut,
    resetAllShortcuts,
    loadCustomKeybindings,
    saveCustomKeybindings,
  };
}
