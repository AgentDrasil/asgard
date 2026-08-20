import { computed } from "vue";

const isMac =
  typeof navigator !== "undefined" &&
  Boolean(navigator.platform && /mac/i.test(navigator.platform));

export function useShortcuts() {
  const modKey = computed(() => (isMac ? "⌘" : "Ctrl"));
  const altKey = computed(() => (isMac ? "⌥" : "Alt"));

  const toggleSidebarShortcut = computed(() => `${modKey.value}+B`);
  const toggleArtifactsShortcut = computed(() => `${modKey.value}+${altKey.value}+B`);
  const toggleDiffShortcut = computed(() => `${modKey.value}+${altKey.value}+D`);
  const toggleTerminalShortcut = computed(() => `${modKey.value}+\``);
  const sendShortcut = computed(() => `${modKey.value}+Enter`);
  const fileSearchShortcut = computed(() => `${modKey.value}+P`);
  const commandPaletteShortcut = computed(() => `${modKey.value}+Shift+P`);
  const commandPaletteF1Shortcut = computed(() => "F1");
  const toggleFileViewShortcut = computed(() => `${modKey.value}+${altKey.value}+F`);
  const findShortcut = computed(() => `${modKey.value}+F`);

  return {
    isMac,
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
    findShortcut,
  };
}
