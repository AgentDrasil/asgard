import type { ITheme } from "@xterm/xterm";
import { latteTheme } from "./latte";
import { frappeTheme } from "./frappe";
import { macchiatoTheme } from "./macchiato";
import { mochaTheme } from "./mocha";

export type CatppuccinThemeKey = "latte" | "frappe" | "macchiato" | "mocha" | "auto";

export interface ThemeOption {
  name: string;
  theme: ITheme;
}

export const CATPPUCCIN_THEMES: Record<Exclude<CatppuccinThemeKey, "auto">, ThemeOption> = {
  latte: {
    name: "Latte (Light)",
    theme: latteTheme,
  },
  frappe: {
    name: "Frappé (Dark)",
    theme: frappeTheme,
  },
  macchiato: {
    name: "Macchiato (Dark)",
    theme: macchiatoTheme,
  },
  mocha: {
    name: "Mocha (Dark)",
    theme: mochaTheme,
  },
};

export { latteTheme, frappeTheme, macchiatoTheme, mochaTheme };
