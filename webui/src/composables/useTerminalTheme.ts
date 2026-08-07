import { ref, computed, onMounted, onUnmounted } from "vue";
import type { ITheme } from "@xterm/xterm";
import { CATPPUCCIN_THEMES, type CatppuccinThemeKey } from "../themes/terminal/catppuccin";

const LOCAL_STORAGE_KEY = "terminal_catppuccin_theme";

export interface TerminalThemeOption {
  key: CatppuccinThemeKey;
  label: string;
}

/**
 * Terminal theme selection, shared between the panel header (selector) and the
 * embedded xterm instance. `activeTheme` resolves the "auto" choice against the
 * active DaisyUI data-theme and updates reactively when it changes.
 */
export function useTerminalTheme() {
  const storedKey = typeof window !== "undefined" ? localStorage.getItem(LOCAL_STORAGE_KEY) : null;
  const selectedThemeKey = ref<CatppuccinThemeKey>((storedKey as CatppuccinThemeKey) || "auto");

  const docTheme = ref<string | null>(
    typeof document !== "undefined" ? document.documentElement.getAttribute("data-theme") : null,
  );

  const activeTheme = computed<ITheme>(() => {
    const key = selectedThemeKey.value;
    if (key === "auto") {
      return docTheme.value === "light"
        ? CATPPUCCIN_THEMES.latte.theme
        : CATPPUCCIN_THEMES.macchiato.theme;
    }
    return CATPPUCCIN_THEMES[key]?.theme ?? CATPPUCCIN_THEMES.macchiato.theme;
  });

  const setThemeKey = (key: CatppuccinThemeKey) => {
    selectedThemeKey.value = key;
    if (key === "auto") localStorage.removeItem(LOCAL_STORAGE_KEY);
    else localStorage.setItem(LOCAL_STORAGE_KEY, key);
  };

  const themeOptions: TerminalThemeOption[] = [
    { key: "auto", label: "Auto (DaisyUI)" },
    ...(
      Object.entries(CATPPUCCIN_THEMES) as [
        Exclude<CatppuccinThemeKey, "auto">,
        (typeof CATPPUCCIN_THEMES)[keyof typeof CATPPUCCIN_THEMES],
      ][]
    ).map(([key, val]) => ({ key, label: `Catppuccin ${val.name}` })),
  ];

  let observer: MutationObserver | null = null;
  onMounted(() => {
    observer = new MutationObserver(() => {
      docTheme.value = document.documentElement.getAttribute("data-theme");
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
  });
  onUnmounted(() => {
    observer?.disconnect();
    observer = null;
  });

  return { selectedThemeKey, activeTheme, setThemeKey, themeOptions };
}
