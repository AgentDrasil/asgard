// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { createApp, nextTick, h } from "vue";
import LanguageSelector from "./LanguageSelector.vue";
import { i18n, setLocale, getLocale, LOCALE_STORAGE_KEY } from "../../i18n";

describe("LanguageSelector.vue", () => {
  let root: HTMLElement;
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
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
    if (originalLocalStorage) {
      Object.defineProperty(window, "localStorage", {
        value: originalLocalStorage,
        configurable: true,
        writable: true,
      });
    }
  });

  it("renders language select options and reflects active locale", async () => {
    const app = createApp({
      render() {
        return h(LanguageSelector);
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const select = root.querySelector("select") as HTMLSelectElement;
    expect(select).not.toBeNull();
    expect(select.value).toBe("en");

    const options = Array.from(root.querySelectorAll("option"));
    expect(options.length).toBe(2);
    expect(options[0].value).toBe("en");
    expect(options[0].textContent?.trim()).toBe("English");
    expect(options[1].value).toBe("zh-CN");
    expect(options[1].textContent?.trim()).toBe("简体中文");

    app.unmount();
  });

  it("switches locale and saves to localStorage on change event", async () => {
    const app = createApp({
      render() {
        return h(LanguageSelector);
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const select = root.querySelector("select") as HTMLSelectElement;
    expect(select).not.toBeNull();

    select.value = "zh-CN";
    select.dispatchEvent(new Event("change"));
    await nextTick();

    expect(getLocale()).toBe("zh-CN");
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("zh-CN");

    app.unmount();
  });
});
