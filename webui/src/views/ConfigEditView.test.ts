// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import ConfigEditView from "./ConfigEditView.vue";
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
  onBeforeRouteLeave: vi.fn<() => void>(),
}));

describe("ConfigEditView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");

    vi.spyOn(api, "getConfigFile").mockResolvedValue({
      path: "/app/config.yaml",
      content: "server:\n  port: 8080\n",
      exists: true,
    });
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

  it("renders configuration editor in en", async () => {
    const app = createApp({
      render() {
        return h(ConfigEditView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("Configuration Editor");
    expect(root.textContent).toContain("Reload");
    expect(root.textContent).toContain("Save");

    app.unmount();
  });

  it("renders configuration editor in zh-CN", async () => {
    setLocale("zh-CN");
    const app = createApp({
      render() {
        return h(ConfigEditView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("配置文件编辑器");
    expect(root.textContent).toContain("重新读取");
    expect(root.textContent).toContain("保存");

    app.unmount();
  });
});
