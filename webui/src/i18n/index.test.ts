// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  setLocale,
  getLocale,
  isValidLocale,
  initI18nWithBackend,
  LOCALE_STORAGE_KEY,
  t,
} from "./index";

describe("i18n core", () => {
  let mockStorage: Record<string, string> = {};
  let originalLocalStorage: Storage | undefined;

  beforeEach(() => {
    mockStorage = {};
    const storageMock: Storage = {
      getItem: (key: string) => mockStorage[key] || null,
      setItem: (key: string, value: string) => {
        mockStorage[key] = value;
      },
      removeItem: (key: string) => {
        delete mockStorage[key];
      },
      clear: () => {
        mockStorage = {};
      },
      key: (index: number) => Object.keys(mockStorage)[index] || null,
      get length() {
        return Object.keys(mockStorage).length;
      },
    };

    originalLocalStorage = window.localStorage;
    Object.defineProperty(window, "localStorage", {
      value: storageMock,
      configurable: true,
      writable: true,
    });

    setLocale("en", false);
  });

  afterEach(() => {
    if (originalLocalStorage) {
      Object.defineProperty(window, "localStorage", {
        value: originalLocalStorage,
        configurable: true,
        writable: true,
      });
    }
  });

  it("identifies valid and invalid locales", () => {
    expect(isValidLocale("en")).toBe(true);
    expect(isValidLocale("zh-CN")).toBe(true);
    expect(isValidLocale("fr")).toBe(false);
    expect(isValidLocale("")).toBe(false);
    expect(isValidLocale(null)).toBe(false);
    expect(isValidLocale(undefined)).toBe(false);
  });

  it("switches locale and translates keys", () => {
    setLocale("en", false);
    expect(getLocale()).toBe("en");
    expect(t("common.save")).toBe("Save");
    expect(t("common.cancel")).toBe("Cancel");

    setLocale("zh-CN", false);
    expect(getLocale()).toBe("zh-CN");
    expect(t("common.save")).toBe("保存");
    expect(t("common.cancel")).toBe("取消");
  });

  it("persists locale to localStorage when persist is true", () => {
    setLocale("zh-CN", true);
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("zh-CN");

    setLocale("en", true);
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en");
  });

  it("does not persist to localStorage when persist is false", () => {
    setLocale("zh-CN", false);
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });

  it("rejects invalid locale and retains current locale", () => {
    setLocale("en", false);
    const result = setLocale("invalid_lang");
    expect(result).toBe(false);
    expect(getLocale()).toBe("en");
  });

  it("initI18nWithBackend applies default_ui_lang if user has no saved preference", () => {
    setLocale("en", false);
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();

    initI18nWithBackend("zh-CN");
    expect(getLocale()).toBe("zh-CN");
    // Should NOT write to localStorage
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });

  it("initI18nWithBackend does NOT override explicit user preference in localStorage", () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, "en");
    setLocale("en", false);

    initI18nWithBackend("zh-CN");
    // Should remain "en" because localStorage has user choice
    expect(getLocale()).toBe("en");
  });

  it("initI18nWithBackend ignores invalid backend default_ui_lang", () => {
    setLocale("en", false);
    initI18nWithBackend("invalid_locale");
    expect(getLocale()).toBe("en");
  });
});
