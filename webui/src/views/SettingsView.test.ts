// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import SettingsView from "./SettingsView.vue";
import * as api from "../lib/api";
import { i18n, setLocale } from "../i18n";
import { __resetUserProfileForTest } from "../composables/useUserProfile";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock vue-router
const mockPush = vi.fn<() => void>();
const mockBack = vi.fn<() => void>();
vi.mock("vue-router", () => ({
  useRouter: () => ({
    push: mockPush,
    back: mockBack,
  }),
}));

describe("SettingsView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    __resetUserProfileForTest();
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");

    vi.spyOn(api, "getRunners").mockResolvedValue(null);
    vi.spyOn(api, "getManageRunners").mockResolvedValue(null);
    vi.spyOn(api, "getSystemLogs").mockResolvedValue([
      {
        id: 1,
        timestamp: new Date().toISOString(),
        level: "error",
        source: "backend",
        message: "err 1",
      },
      {
        id: 2,
        timestamp: new Date().toISOString(),
        level: "warn",
        source: "backend",
        message: "warn 1",
      },
    ]);
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
    setLocale("en");
  });

  const flush = async () => {
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();
  };

  it("renders settings header, preferences, language selector, and system actions in en", async () => {
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("Settings");
    expect(root.textContent).toContain("Preferences");
    expect(root.textContent).toContain("Language");
    expect(root.textContent).toContain("Theme");
    expect(root.textContent).toContain("System & Diagnostics");
    expect(root.textContent).toContain("Personal API Keys");
    expect(root.textContent).toContain("Runners");
    expect(root.textContent).toContain("Usage & Quota");
    expect(root.textContent).toContain("Account & Access");
    expect(root.textContent).toContain("Compression Statistics");

    app.unmount();
  });

  it("renders in zh-CN when locale is switched", async () => {
    setLocale("zh-CN");
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("设置");
    expect(root.textContent).toContain("偏好设置");
    expect(root.textContent).toContain("语言");
    expect(root.textContent).toContain("主题");
    expect(root.textContent).toContain("系统与诊断");
    expect(root.textContent).toContain("个人 API 密钥");
    expect(root.textContent).toContain("Runner 节点");
    expect(root.textContent).toContain("用量与配额");
    expect(root.textContent).toContain("账号与访问");
    expect(root.textContent).toContain("压缩统计");

    app.unmount();
  });

  it("shows the authentication section only when the backend requires auth", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(true);
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("Account & Access");
    expect(root.textContent).toContain("Signed in via OIDC");

    app.unmount();
  });

  it("hides the authentication section when the backend has no auth", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(false);
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).not.toContain("Signed in via OIDC");

    app.unmount();
  });

  it("logs out from the authentication section", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(true);
    const logoutSpy = vi.spyOn(api, "logout").mockImplementation(() => {});
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const buttons = Array.from(root.querySelectorAll("button"));
    const signOut = buttons.find((b) => b.textContent?.includes("Sign out"));
    expect(signOut).toBeDefined();
    signOut!.click();

    expect(logoutSpy).toHaveBeenCalledTimes(1);
    app.unmount();
  });

  it("navigates to /settings/keys when Personal API Keys card is clicked", async () => {
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const keysCard = root.querySelector<HTMLElement>('[data-testid="nav-proxy-keys"]');
    expect(keysCard).not.toBeNull();
    keysCard!.click();

    expect(mockPush).toHaveBeenCalledWith("/settings/keys");
    app.unmount();
  });

  it("navigates to /settings/runners when Runners card is clicked", async () => {
    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const runnersCard = root.querySelector<HTMLElement>('[data-testid="nav-runners"]');
    expect(runnersCard).not.toBeNull();
    runnersCard!.click();

    expect(mockPush).toHaveBeenCalledWith("/settings/runners");
    app.unmount();
  });

  it("restricts server logs and diagnostics to admin users", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(true);
    vi.spyOn(api, "apiFetch").mockImplementation(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/auth/me") {
        return {
          ok: true,
          json: async () => ({ subject: "user-1", name: "User", isAdmin: false }),
        } as Response;
      }
      return { ok: true, json: async () => ({}) } as Response;
    });
    const getLogsSpy = vi.spyOn(api, "getSystemLogs");

    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(getLogsSpy).not.toHaveBeenCalled();
    expect(root.textContent).not.toContain("Logs & Diagnostics");
    expect(root.textContent).not.toContain("Edit Config");
    app.unmount();
  });

  it("allows admin users to view server logs and diagnostics", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(true);
    vi.spyOn(api, "apiFetch").mockImplementation(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/auth/me") {
        return {
          ok: true,
          json: async () => ({ subject: "admin-1", name: "Admin", isAdmin: true }),
        } as Response;
      }
      return { ok: true, json: async () => ({}) } as Response;
    });
    const getLogsSpy = vi.spyOn(api, "getSystemLogs").mockResolvedValue([
      {
        id: 1,
        timestamp: new Date().toISOString(),
        level: "warn",
        source: "backend",
        message: "w",
      },
    ]);

    const app = createApp({
      render() {
        return h(SettingsView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(getLogsSpy).toHaveBeenCalled();
    expect(root.textContent).toContain("Logs & Diagnostics");
    app.unmount();
  });
});
