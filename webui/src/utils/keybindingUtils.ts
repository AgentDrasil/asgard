import type { KeybindingActionDef, SupportedOS } from "../types";

export const DEFAULT_KEYBINDING_ACTIONS: KeybindingActionDef[] = [
  {
    id: "toggle_sidebar",
    title: "Toggle Sidebar",
    description: "Expand or collapse the left navigation sidebar",
    category: "navigation",
    defaultKeys: {
      linux: "Ctrl+B",
      windows: "Ctrl+B",
      mac: "Cmd+B",
    },
  },
  {
    id: "toggle_artifacts",
    title: "Toggle Artifacts / Side Panel",
    description:
      "Toggle artifacts panel in Chat view, file tree in File view, or changes list in VCS view",
    category: "panel",
    defaultKeys: {
      linux: "Ctrl+Alt+B",
      windows: "Ctrl+Alt+B",
      mac: "Cmd+Alt+B",
    },
  },
  {
    id: "toggle_diff",
    title: "Toggle VCS Diff View",
    description: "Switch to VCS Diff view or toggle changes panel",
    category: "navigation",
    defaultKeys: {
      linux: "Ctrl+Alt+D",
      windows: "Ctrl+Alt+D",
      mac: "Cmd+Alt+D",
    },
  },
  {
    id: "toggle_file_view",
    title: "Toggle File View",
    description: "Switch to File Browser view",
    category: "navigation",
    defaultKeys: {
      linux: "Ctrl+Alt+F",
      windows: "Ctrl+Alt+F",
      mac: "Cmd+Alt+F",
    },
  },
  {
    id: "toggle_terminal",
    title: "Toggle Terminal",
    description: "Open or close the embedded session terminal",
    category: "panel",
    defaultKeys: {
      linux: "Ctrl+Backquote",
      windows: "Ctrl+Backquote",
      mac: "Cmd+Backquote",
    },
  },
  {
    id: "command_palette",
    title: "Command Palette",
    description: "Open the command palette for quick action search",
    category: "general",
    defaultKeys: {
      linux: ["Ctrl+P", "Ctrl+Shift+P", "F1"],
      windows: ["Ctrl+P", "Ctrl+Shift+P", "F1"],
      mac: ["Cmd+P", "Cmd+Shift+P", "F1"],
    },
  },
  {
    id: "find",
    title: "Find in Page / Chat / Code",
    description: "Open find dialog within active chat or file code viewer",
    category: "general",
    defaultKeys: {
      linux: "Ctrl+F",
      windows: "Ctrl+F",
      mac: "Cmd+F",
    },
  },
  {
    id: "send_message",
    title: "Send Chat Message",
    description: "Submit user message in chat input box",
    category: "chat",
    defaultKeys: {
      linux: "Ctrl+Enter",
      windows: "Ctrl+Enter",
      mac: "Cmd+Enter",
    },
  },
  {
    id: "new_chat",
    title: "New Chat Session",
    description: "Create a new conversation session",
    category: "chat",
    defaultKeys: {
      linux: "Ctrl+Alt+N",
      windows: "Ctrl+Alt+N",
      mac: "Cmd+Alt+N",
    },
  },
];

export function getDefaultBindingsForOS(os: SupportedOS): Record<string, string[]> {
  const result: Record<string, string[]> = {};
  for (const action of DEFAULT_KEYBINDING_ACTIONS) {
    const raw = action.defaultKeys[os];
    if (Array.isArray(raw)) {
      result[action.id] = raw.map(normalizeShortcut);
    } else if (typeof raw === "string" && raw.trim() !== "") {
      result[action.id] = [normalizeShortcut(raw)];
    } else {
      result[action.id] = [];
    }
  }
  return result;
}

export function normalizeShortcut(shortcut: string): string {
  if (!shortcut || typeof shortcut !== "string") return "";
  const trimmed = shortcut.trim();
  if (!trimmed) return "";

  const parts = trimmed
    .split("+")
    .map((p) => p.trim())
    .filter(Boolean);
  if (parts.length === 0) return "";

  let ctrl = false;
  let cmd = false;
  let alt = false;
  let shift = false;
  let keyPart = "";

  for (const part of parts) {
    const lower = part.toLowerCase();
    if (lower === "ctrl" || lower === "control") {
      ctrl = true;
    } else if (lower === "cmd" || lower === "command" || lower === "meta" || part === "⌘") {
      cmd = true;
    } else if (lower === "alt" || lower === "option" || lower === "opt" || part === "⌥") {
      alt = true;
    } else if (lower === "shift" || part === "⇧") {
      shift = true;
    } else {
      // Base key
      keyPart = normalizeBaseKey(part);
    }
  }

  const result: string[] = [];
  if (ctrl) result.push("Ctrl");
  if (cmd) result.push("Cmd");
  if (alt) result.push("Alt");
  if (shift) result.push("Shift");

  if (keyPart) {
    result.push(keyPart);
  }

  return result.join("+");
}

function normalizeBaseKey(key: string): string {
  if (!key) return "";
  if (key === "`" || key.toLowerCase() === "backquote") return "Backquote";
  if (key.length === 1) {
    return key.toUpperCase();
  }

  const lower = key.toLowerCase();
  const functionKeyMatch = /^f([1-9]|1[0-2])$/i.exec(key);
  if (functionKeyMatch) {
    return `F${functionKeyMatch[1]}`;
  }

  switch (lower) {
    case "enter":
    case "return":
      return "Enter";
    case "escape":
    case "esc":
      return "Escape";
    case "tab":
      return "Tab";
    case "space":
    case "spacebar":
      return "Space";
    case "arrowup":
    case "up":
      return "ArrowUp";
    case "arrowdown":
    case "down":
      return "ArrowDown";
    case "arrowleft":
    case "left":
      return "ArrowLeft";
    case "arrowright":
    case "right":
      return "ArrowRight";
    case "backspace":
      return "Backspace";
    case "delete":
    case "del":
      return "Delete";
    case "minus":
    case "-":
      return "Minus";
    case "equal":
    case "=":
      return "Equal";
    case "bracketleft":
    case "[":
      return "BracketLeft";
    case "bracketright":
    case "]":
      return "BracketRight";
    case "semicolon":
    case ";":
      return "Semicolon";
    case "quote":
    case "'":
      return "Quote";
    case "comma":
    case ",":
      return "Comma";
    case "period":
    case ".":
      return "Period";
    case "slash":
    case "/":
      return "Slash";
    case "backslash":
    case "\\":
      return "Backslash";
    default:
      // Capitalize first letter
      return key.charAt(0).toUpperCase() + key.slice(1);
  }
}

export function isSingleShortcutMatch(
  event: KeyboardEvent,
  shortcut: string,
  os: SupportedOS,
): boolean {
  const normalized = normalizeShortcut(shortcut);
  if (!normalized) return false;

  const parts = normalized.split("+");
  const expectedCtrl = parts.includes("Ctrl");
  const expectedCmd = parts.includes("Cmd");
  const expectedAlt = parts.includes("Alt");
  const expectedShift = parts.includes("Shift");

  // In non-mac, Cmd is not expected (or maps to metaKey)
  // In Mac: Cmd -> metaKey, Ctrl -> ctrlKey
  const actualCtrl = Boolean(event.ctrlKey);
  const actualMeta = Boolean(event.metaKey);
  const actualAlt = Boolean(event.altKey);
  const actualShift = Boolean(event.shiftKey);

  let ctrlMatches = false;
  let metaMatches = false;

  if (os === "mac") {
    ctrlMatches = actualCtrl === expectedCtrl;
    metaMatches = actualMeta === expectedCmd;
  } else {
    // Windows / Linux: Ctrl in shortcut maps to ctrlKey. If Cmd is present, metaKey must match.
    ctrlMatches = actualCtrl === expectedCtrl;
    metaMatches = actualMeta === expectedCmd;
  }

  if (!ctrlMatches || !metaMatches || actualAlt !== expectedAlt || actualShift !== expectedShift) {
    return false;
  }

  const expectedBaseKey = parts[parts.length - 1];
  // If the shortcut is only modifiers (which shouldn't happen normally), compare fails
  if (["Ctrl", "Cmd", "Alt", "Shift"].includes(expectedBaseKey)) {
    return false;
  }

  return matchEventBaseKey(event, expectedBaseKey);
}

function matchEventBaseKey(event: KeyboardEvent, expectedKey: string): boolean {
  const code = event.code;
  const key = event.key;

  if (expectedKey === "Backquote") {
    return code === "Backquote" || key === "`" || key === "~";
  }

  if (/^F([1-9]|1[0-2])$/.test(expectedKey)) {
    return code === expectedKey || key === expectedKey;
  }

  if (expectedKey === "Enter") {
    return code === "Enter" || code === "NumpadEnter" || key === "Enter";
  }

  if (expectedKey === "Escape") {
    return code === "Escape" || key === "Escape" || key === "Esc";
  }

  if (expectedKey === "Tab") {
    return code === "Tab" || key === "Tab";
  }

  if (expectedKey === "Space") {
    return code === "Space" || key === " " || key === "Spacebar";
  }

  if (expectedKey === "ArrowUp") return code === "ArrowUp" || key === "ArrowUp";
  if (expectedKey === "ArrowDown") return code === "ArrowDown" || key === "ArrowDown";
  if (expectedKey === "ArrowLeft") return code === "ArrowLeft" || key === "ArrowLeft";
  if (expectedKey === "ArrowRight") return code === "ArrowRight" || key === "ArrowRight";

  if (expectedKey.length === 1 && /^[A-Z0-9]$/.test(expectedKey)) {
    if (
      code === `Key${expectedKey}` ||
      code === `Digit${expectedKey}` ||
      code === `Numpad${expectedKey}`
    ) {
      return true;
    }
    return key.toUpperCase() === expectedKey;
  }

  return (
    code.toLowerCase() === expectedKey.toLowerCase() ||
    key.toLowerCase() === expectedKey.toLowerCase()
  );
}

export function isKeyboardEventMatch(
  event: KeyboardEvent,
  shortcut: string | string[],
  os: SupportedOS,
): boolean {
  if (!shortcut) return false;
  if (Array.isArray(shortcut)) {
    if (shortcut.length === 0) return false;
    return shortcut.some((s) => isSingleShortcutMatch(event, s, os));
  }
  if (typeof shortcut === "string") {
    if (shortcut.trim() === "") return false;
    return isSingleShortcutMatch(event, shortcut, os);
  }
  return false;
}

export function formatShortcutDisplay(shortcut: string | string[], os: SupportedOS): string {
  if (!shortcut) return "Unassigned";
  if (Array.isArray(shortcut)) {
    if (shortcut.length === 0) return "Unassigned";
    return shortcut.map((s) => formatSingleShortcut(s, os)).join(" / ");
  }
  if (typeof shortcut === "string") {
    if (shortcut.trim() === "") return "Unassigned";
    return formatSingleShortcut(shortcut, os);
  }
  return "Unassigned";
}

function formatSingleShortcut(shortcut: string, os: SupportedOS): string {
  const normalized = normalizeShortcut(shortcut);
  if (!normalized) return "Unassigned";

  const parts = normalized.split("+");
  const formattedParts: string[] = [];

  for (const part of parts) {
    if (part === "Cmd") {
      formattedParts.push(os === "mac" ? "⌘" : "Cmd");
    } else if (part === "Alt") {
      formattedParts.push(os === "mac" ? "⌥" : "Alt");
    } else if (part === "Ctrl") {
      formattedParts.push("Ctrl");
    } else if (part === "Shift") {
      formattedParts.push("Shift");
    } else if (part === "Backquote") {
      formattedParts.push("`");
    } else {
      formattedParts.push(part);
    }
  }

  return formattedParts.join("+");
}

export function detectConflicts(
  bindings: Record<string, string | string[]>,
): Map<string, string[]> {
  // Map of normalizedShortcut -> Array of actionIds
  const shortcutToAction = new Map<string, string[]>();

  for (const [actionId, rawKeys] of Object.entries(bindings)) {
    if (!rawKeys) continue;
    const keys = Array.isArray(rawKeys) ? rawKeys : [rawKeys];
    for (const key of keys) {
      if (!key) continue;
      const normalized = normalizeShortcut(key);
      if (!normalized) continue; // Skip empty / unassigned

      const existing = shortcutToAction.get(normalized) || [];
      if (!existing.includes(actionId)) {
        existing.push(actionId);
      }
      shortcutToAction.set(normalized, existing);
    }
  }

  // Filter only shortcuts with > 1 action
  const conflicts = new Map<string, string[]>();
  for (const [shortcut, actions] of shortcutToAction.entries()) {
    if (actions.length > 1) {
      conflicts.set(shortcut, actions);
    }
  }
  return conflicts;
}

export function calculateDelta(
  current: Record<string, string | string[]>,
  defaults: Record<string, string | string[]>,
): Record<string, string | string[]> {
  const delta: Record<string, string | string[]> = {};

  for (const [actionId, curVal] of Object.entries(current)) {
    const defVal = defaults[actionId];

    const curNorm = normalizeValue(curVal);
    const defNorm = normalizeValue(defVal);

    if (!isEqualBindings(curNorm, defNorm)) {
      delta[actionId] = curVal;
    }
  }

  return delta;
}

function normalizeValue(val: string | string[] | undefined): string[] {
  if (val === undefined || val === null) return [];
  if (Array.isArray(val)) {
    return val.map(normalizeShortcut).filter(Boolean);
  }
  if (typeof val === "string") {
    const n = normalizeShortcut(val);
    return n ? [n] : [];
  }
  return [];
}

function isEqualBindings(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

export function resolveGlobalAction(
  event: KeyboardEvent,
  activeBindings: Record<string, string[]>,
  isInputFocused: boolean,
  isTerminalOpen: boolean,
  os: SupportedOS,
): string | null {
  // Pre-guard actions: command_palette, toggle_terminal
  const commandPaletteKeys = activeBindings["command_palette"] || [];
  if (isKeyboardEventMatch(event, commandPaletteKeys, os)) {
    event.preventDefault();
    event.stopPropagation();
    return "command_palette";
  }

  const toggleTerminalKeys = activeBindings["toggle_terminal"] || [];
  if (isKeyboardEventMatch(event, toggleTerminalKeys, os)) {
    event.preventDefault();
    event.stopPropagation();
    return isTerminalOpen ? "close_terminal" : "open_terminal_session";
  }

  // Input guard: post-guard actions are suppressed when input is focused
  if (isInputFocused) {
    return null;
  }

  // Post-guard actions: toggle_sidebar, toggle_artifacts, toggle_diff, toggle_file_view, new_chat
  const postGuardActions = [
    "toggle_sidebar",
    "toggle_artifacts",
    "toggle_diff",
    "toggle_file_view",
    "new_chat",
  ];

  for (const actionId of postGuardActions) {
    const keys = activeBindings[actionId] || [];
    if (isKeyboardEventMatch(event, keys, os)) {
      event.preventDefault();
      event.stopPropagation();
      return actionId;
    }
  }

  return null;
}
