// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import LogView from "./LogView.vue";
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

describe("LogView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    __resetUserProfileForTest();
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");

    vi.spyOn(api, "getSystemLogs").mockResolvedValue([
      {
        id: 1,
        timestamp: new Date().toISOString(),
        level: "error",
        source: "backend",
        message: "Failed to connect",
        details: "Connection refused on 127.0.0.1:8080",
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

  it("renders log viewer in en", async () => {
    const app = createApp({
      render() {
        return h(LogView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("System Logs & Diagnostics");
    expect(root.textContent).toContain("Total Entries");
    expect(root.textContent).toContain("Errors");
    expect(root.textContent).toContain("Warnings");
    expect(root.textContent).toContain("Refresh");

    app.unmount();
  });

  it("renders log viewer in zh-CN", async () => {
    setLocale("zh-CN");
    const app = createApp({
      render() {
        return h(LogView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("系统日志与诊断");
    expect(root.textContent).toContain("日志总数");
    expect(root.textContent).toContain("错误数");
    expect(root.textContent).toContain("警告数");
    expect(root.textContent).toContain("刷新");

    app.unmount();
  });

  it("shows access denied and skips fetching logs for non-admin", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(true);
    vi.spyOn(api, "apiFetch").mockImplementation(async (path: string) => {
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
        return h(LogView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(getLogsSpy).not.toHaveBeenCalled();
    const denied = root.querySelector('[data-testid="logs-access-denied"]');
    expect(denied).not.toBeNull();
    expect(denied!.textContent).toContain("Administrator Access Required");
    app.unmount();
  });
});
