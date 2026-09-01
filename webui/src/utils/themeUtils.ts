export const LIGHT_THEMES = ["light", "latte", "cupcake"] as const;
export type LightTheme = (typeof LIGHT_THEMES)[number];

/**
 * Returns true if the provided theme name corresponds to a known light theme.
 */
export function isLightTheme(docTheme: string | null | undefined): boolean {
  if (!docTheme) return false;
  return (LIGHT_THEMES as readonly string[]).includes(docTheme.toLowerCase());
}

/**
 * Returns true if the active/provided theme is dark.
 */
export function isDarkTheme(docTheme?: string | null | undefined): boolean {
  const currentTheme =
    docTheme !== undefined
      ? docTheme
      : typeof document !== "undefined"
        ? document.documentElement.getAttribute("data-theme")
        : "dark";
  return !isLightTheme(currentTheme);
}
