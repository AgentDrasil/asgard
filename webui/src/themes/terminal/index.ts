import type { ITheme } from "@xterm/xterm";
import { CATPPUCCIN_THEMES } from "./catppuccin";
import { DAISYUI_TERMINAL_THEMES, type DaisyUIThemeKey } from "./daisyui";

export interface AppThemeItem {
  id: string;
  name: string;
  group: "DaisyUI Themes" | "Catppuccin Themes";
}

export const APP_THEMES: AppThemeItem[] = [
  ...Object.entries(DAISYUI_TERMINAL_THEMES).map(([id, val]) => ({
    id,
    name: val.name,
    group: "DaisyUI Themes" as const,
  })),
  ...Object.entries(CATPPUCCIN_THEMES).map(([id, val]) => ({
    id,
    name: `Catppuccin ${val.name}`,
    group: "Catppuccin Themes" as const,
  })),
];

export function resolveTerminalTheme(docTheme: string | null): ITheme {
  const currentDocTheme = docTheme || "dark";
  if (currentDocTheme in DAISYUI_TERMINAL_THEMES) {
    return DAISYUI_TERMINAL_THEMES[currentDocTheme as DaisyUIThemeKey].theme;
  }
  if (currentDocTheme in CATPPUCCIN_THEMES) {
    return CATPPUCCIN_THEMES[currentDocTheme as keyof typeof CATPPUCCIN_THEMES].theme;
  }
  return DAISYUI_TERMINAL_THEMES.dark.theme;
}
