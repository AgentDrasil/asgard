import { createI18n, useI18n } from "vue-i18n";
import en from "./locales/en";
import zhCN from "./locales/zh-CN";

// Re-export useI18n for convenient module consumption and backwards compatibility
export { useI18n };

export type SupportedLocale = "en" | "zh-CN";
export const SUPPORTED_LOCALES: { value: SupportedLocale; label: string }[] = [
  { value: "en", label: "English" },
  { value: "zh-CN", label: "简体中文" },
];

export const LOCALE_STORAGE_KEY = "asgard_locale";

export function getSavedLocale(): string | null {
  try {
    return localStorage.getItem(LOCALE_STORAGE_KEY);
  } catch {
    return null;
  }
}

export function isValidLocale(locale: string | null | undefined): locale is SupportedLocale {
  return locale === "en" || locale === "zh-CN";
}

export function getInitialLocale(): SupportedLocale {
  const saved = getSavedLocale();
  if (isValidLocale(saved)) {
    return saved;
  }
  return "en";
}

export const i18n = createI18n({
  legacy: false,
  locale: getInitialLocale(),
  fallbackLocale: "en",
  messages: {
    en,
    "zh-CN": zhCN,
  },
});

export function setLocale(locale: string, persist = true): boolean {
  if (!isValidLocale(locale)) {
    return false;
  }
  (i18n.global.locale as any).value = locale;
  if (persist) {
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    } catch (e) {
      console.warn("Failed to persist locale to localStorage:", e);
    }
  }
  return true;
}

export function getLocale(): SupportedLocale {
  return (i18n.global.locale as any).value as SupportedLocale;
}

export function initI18nWithBackend(defaultUILang?: string): void {
  const saved = getSavedLocale();
  // Only apply backend default if user hasn't explicitly set a local preference
  if (!saved && isValidLocale(defaultUILang)) {
    setLocale(defaultUILang, false);
  }
}

export const t = i18n.global.t;
export default i18n;
