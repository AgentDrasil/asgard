// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import RunnersView from "./RunnersView.vue";
import * as api from "../lib/api";
import { i18n, setLocale } from "../i18n";
import { resetRunnersForTest } from "../composables/useRunners";
import { __resetUserProfileForTest } from "../composables/useUserProfile";

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

describe("RunnersView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    mockPush.mockReset();
    mockBack.mockReset();
    setLocale("en");
    resetRunnersForTest();
    __resetUserProfileForTest();

    vi.spyOn(api, "getRunners").mockResolvedValue({
      backend_version: "v1.0.0",
      runners: [{ node_id: "node-test-1", online: true, version: "v1.0" }],
    });
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [{ node_id: "node-test-1", online: true, users: [] }],
      users: [{ subject: "user-1", name: "Alice", email: "alice@example.com" }],
    });
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
  });

  const flush = async () => {
    for (let i = 0; i < 5; i++) {
      await Promise.resolve();
      await nextTick();
    }
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();
  };

  it("renders header and runners panel for standard users", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(true);
    vi.spyOn(api, "apiFetch").mockImplementation(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/auth/me") {
        return {
          ok: true,
          json: async () => ({ subject: "user-regular", name: "User", isAdmin: false }),
        } as Response;
      }
      return { ok: true, json: async () => ({}) } as Response;
    });

    const app = createApp({
      render() {
        return h(RunnersView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("Runners");
    expect(root.textContent).toContain("node-test-1");
    // Non-admin should NOT see the admin management section
    expect(root.querySelector('[data-testid="runner-admin-panel"]')).toBeNull();
    app.unmount();
  });

  it("renders admin management panel for admin users", async () => {
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

    const app = createApp({
      render() {
        return h(RunnersView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    expect(root.textContent).toContain("Runners");
    expect(root.querySelector('[data-testid="runner-admin-panel"]')).not.toBeNull();
    app.unmount();
  });

  it("navigates back to settings when back button is clicked", async () => {
    vi.spyOn(api, "isAuthEnabled").mockReturnValue(false);
    const app = createApp({
      render() {
        return h(RunnersView);
      },
    });
    app.use(i18n);
    app.mount(root);
    await flush();

    const backBtn = root.querySelector<HTMLButtonElement>('[data-testid="runners-back-btn"]');
    expect(backBtn).not.toBeNull();
    backBtn!.click();
    expect(mockPush).toHaveBeenCalledWith("/settings");
    app.unmount();
  });
});
