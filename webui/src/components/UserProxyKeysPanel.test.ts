// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { createApp, h, nextTick } from "vue";
import { createI18n } from "vue-i18n";
import UserProxyKeysPanel from "./UserProxyKeysPanel.vue";
import * as api from "../lib/api";
import en from "../i18n/locales/en";
import zhCN from "../i18n/locales/zh-CN";

const i18n = createI18n({
  legacy: false,
  locale: "en",
  messages: { en, "zh-CN": zhCN },
});

const flush = async () => {
  await nextTick();
  await new Promise((r) => setTimeout(r, 10));
};

describe("UserProxyKeysPanel.vue", () => {
  let root: HTMLDivElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders empty state when no keys exist", async () => {
    vi.spyOn(api, "getUserProxyKeys").mockResolvedValue([]);

    const app = createApp({
      render() {
        return h(UserProxyKeysPanel);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const empty = root.querySelector('[data-testid="proxy-keys-empty"]');
    expect(empty).not.toBeNull();
    expect(empty?.textContent).toContain("No personal API keys configured yet");

    app.unmount();
  });

  it("renders configured keys", async () => {
    vi.spyOn(api, "getUserProxyKeys").mockResolvedValue([
      { key_name: "GEMINI_API_KEY", updated_at: new Date().toISOString() },
      { key_name: "CUSTOM_PROXY_KEY", updated_at: new Date().toISOString() },
    ]);

    const app = createApp({
      render() {
        return h(UserProxyKeysPanel);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const rows = root.querySelectorAll('[data-testid="proxy-key-row"]');
    expect(rows.length).toBe(2);
    expect(root.textContent).toContain("GEMINI_API_KEY");
    expect(root.textContent).toContain("CUSTOM_PROXY_KEY");

    app.unmount();
  });

  it("opens modal and saves a new key", async () => {
    vi.spyOn(api, "getUserProxyKeys").mockResolvedValue([]);
    const saveSpy = vi.spyOn(api, "saveUserProxyKey").mockResolvedValue(undefined);

    const app = createApp({
      render() {
        return h(UserProxyKeysPanel);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const addBtn = root.querySelector('[data-testid="add-proxy-key-btn"]') as HTMLButtonElement;
    expect(addBtn).not.toBeNull();
    addBtn.click();
    await flush();

    const modal = document.body.querySelector('[data-testid="proxy-key-modal"]');
    expect(modal).not.toBeNull();

    const nameInput = modal?.querySelector(
      '[data-testid="proxy-key-name-input"]',
    ) as HTMLInputElement;
    const valInput = modal?.querySelector(
      '[data-testid="proxy-key-value-input"]',
    ) as HTMLInputElement;
    nameInput.value = "ANTHROPIC_API_KEY";
    nameInput.dispatchEvent(new Event("input"));
    valInput.value = "sk-ant-test";
    valInput.dispatchEvent(new Event("input"));
    await flush();

    const saveBtn = modal?.querySelector('[data-testid="proxy-key-save-btn"]') as HTMLButtonElement;
    saveBtn.click();
    await flush();

    expect(saveSpy).toHaveBeenCalledWith("ANTHROPIC_API_KEY", "sk-ant-test");

    app.unmount();
  });

  it("validates key name and shows error for invalid or reserved names", async () => {
    vi.spyOn(api, "getUserProxyKeys").mockResolvedValue([]);
    const saveSpy = vi.spyOn(api, "saveUserProxyKey").mockResolvedValue(undefined);

    const app = createApp({
      render() {
        return h(UserProxyKeysPanel);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const addBtn = root.querySelector('[data-testid="add-proxy-key-btn"]') as HTMLButtonElement;
    addBtn.click();
    await flush();

    const modal = document.body.querySelector('[data-testid="proxy-key-modal"]');
    const nameInput = modal?.querySelector(
      '[data-testid="proxy-key-name-input"]',
    ) as HTMLInputElement;
    const valInput = modal?.querySelector(
      '[data-testid="proxy-key-value-input"]',
    ) as HTMLInputElement;
    const saveBtn = modal?.querySelector('[data-testid="proxy-key-save-btn"]') as HTMLButtonElement;

    // 1. Invalid characters (contains hyphen)
    nameInput.value = "invalid-key-name";
    nameInput.dispatchEvent(new Event("input"));
    valInput.value = "secret";
    valInput.dispatchEvent(new Event("input"));
    await flush();

    let errorEl = modal?.querySelector('[data-testid="proxy-key-name-error"]');
    expect(errorEl).not.toBeNull();
    expect(errorEl?.textContent).toContain("letters, numbers, and underscores");
    expect(saveBtn.disabled).toBe(true);

    // 2. Reserved key name (PATH)
    nameInput.value = "PATH";
    nameInput.dispatchEvent(new Event("input"));
    await flush();

    errorEl = modal?.querySelector('[data-testid="proxy-key-name-error"]');
    expect(errorEl).not.toBeNull();
    expect(errorEl?.textContent).toContain("reserved");
    expect(saveBtn.disabled).toBe(true);

    // 3. Reserved key name (HTTP_PROXY)
    nameInput.value = "http_proxy";
    nameInput.dispatchEvent(new Event("input"));
    await flush();

    errorEl = modal?.querySelector('[data-testid="proxy-key-name-error"]');
    expect(errorEl).not.toBeNull();
    expect(errorEl?.textContent).toContain("reserved");
    expect(saveBtn.disabled).toBe(true);

    // 4. Valid name preserves leading whitespace on secret and trims trailing newline
    nameInput.value = "VALID_SECRET_KEY";
    nameInput.dispatchEvent(new Event("input"));
    valInput.value = "  secret-with-leading-spaces  \r\n";
    valInput.dispatchEvent(new Event("input"));
    await flush();

    expect(modal?.querySelector('[data-testid="proxy-key-name-error"]')).toBeNull();
    expect(saveBtn.disabled).toBe(false);

    saveBtn.click();
    await flush();

    expect(saveSpy).toHaveBeenCalledWith("VALID_SECRET_KEY", "  secret-with-leading-spaces  ");

    app.unmount();
  });

  it("confirms and deletes a key", async () => {
    vi.spyOn(api, "getUserProxyKeys").mockResolvedValue([
      { key_name: "TO_DELETE_KEY", updated_at: new Date().toISOString() },
    ]);
    const deleteSpy = vi.spyOn(api, "deleteUserProxyKey").mockResolvedValue(undefined);

    const app = createApp({
      render() {
        return h(UserProxyKeysPanel);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const delBtn = root.querySelector('[data-testid="delete-proxy-key-btn"]') as HTMLButtonElement;
    delBtn.click();
    await flush();

    const delModal = document.body.querySelector('[data-testid="proxy-key-delete-modal"]');
    expect(delModal).not.toBeNull();
    expect(delModal?.textContent).toContain("TO_DELETE_KEY");

    const confirmBtn = delModal?.querySelector(
      '[data-testid="proxy-key-confirm-delete-btn"]',
    ) as HTMLButtonElement;
    confirmBtn.click();
    await flush();

    expect(deleteSpy).toHaveBeenCalledWith("TO_DELETE_KEY");

    app.unmount();
  });
});
