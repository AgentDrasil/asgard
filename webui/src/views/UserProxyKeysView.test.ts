// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import UserProxyKeysView from "./UserProxyKeysView.vue";
import * as api from "../lib/api";
import { i18n, setLocale } from "../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

const mockPush = vi.fn<() => void>();
const mockBack = vi.fn<() => void>();
vi.mock("vue-router", () => ({
  useRouter: () => ({
    push: mockPush,
    back: mockBack,
  }),
}));

describe("UserProxyKeysView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({ proxy_envs: [] });
    vi.spyOn(api, "getUserProxyKeys").mockResolvedValue([
      { key_name: "OPENAI_API_KEY", updated_at: new Date().toISOString() },
    ]);
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
  });

  const flush = async () => {
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();
  };

  it("renders header and proxy keys panel", async () => {
    const app = createApp({
      render() {
        return h(UserProxyKeysView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("Personal API Keys");
    expect(root.textContent).toContain("OPENAI_API_KEY");
    app.unmount();
  });

  it("navigates back to settings when back button is clicked", async () => {
    const app = createApp({
      render() {
        return h(UserProxyKeysView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const backBtn = root.querySelector<HTMLButtonElement>('[data-testid="keys-back-btn"]');
    expect(backBtn).not.toBeNull();
    backBtn!.click();
    expect(mockPush).toHaveBeenCalledWith("/settings");
    app.unmount();
  });
});
