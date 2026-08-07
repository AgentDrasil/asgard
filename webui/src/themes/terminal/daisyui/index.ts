import type { ITheme } from "@xterm/xterm";
import { darkTheme } from "./dark";
import { lightTheme } from "./light";
import { cupcakeTheme } from "./cupcake";

export type DaisyUIThemeKey = "dark" | "light" | "cupcake";

export interface ThemeOption {
  name: string;
  theme: ITheme;
}

export const DAISYUI_TERMINAL_THEMES: Record<DaisyUIThemeKey, ThemeOption> = {
  dark: {
    name: "Dark",
    theme: darkTheme,
  },
  light: {
    name: "Light",
    theme: lightTheme,
  },
  cupcake: {
    name: "Cupcake",
    theme: cupcakeTheme,
  },
};
