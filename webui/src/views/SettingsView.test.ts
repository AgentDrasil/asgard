// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import SettingsView from "./SettingsView.vue";
import * as api from "../lib/api";
import { i18n, setLocale } from "../i18n";

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
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");

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
    expect(root.textContent).toContain("System & Server Actions");
    expect(root.textContent).toContain("Reload Agents");
    expect(root.textContent).toContain("Restart Server");
    expect(root.textContent).toContain("Usage & Quota");
    expect(root.textContent).toContain("Configuration & Diagnostics");

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
    expect(root.textContent).toContain("系统与服务操作");
    expect(root.textContent).toContain("重载 Agent");
    expect(root.textContent).toContain("重启服务器");
    expect(root.textContent).toContain("用量与配额");
    expect(root.textContent).toContain("配置与诊断");

    app.unmount();
  });
});
