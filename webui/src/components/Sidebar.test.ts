// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import Sidebar from "./Sidebar.vue";
import { i18n, setLocale } from "../i18n";
import type { ChatSession } from "../types";

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
let mockCurrentRoute = { path: "/chat/123" };

vi.mock("vue-router", () => ({
  useRoute: () => mockCurrentRoute,
  useRouter: () => ({
    push: mockPush,
  }),
}));

describe("Sidebar.vue", () => {
  let root: HTMLElement;

  const mockSessions: ChatSession[] = [
    {
      chatID: "sess-1",
      title: "Test Session 1",
      currentAgent: "Coder",
      runDir: "/workspace",
      isRunning: false,
    },
  ];

  beforeEach(() => {
    mockPush.mockReset();
    mockCurrentRoute = { path: "/chat/123" };
    setLocale("en", false);
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.body.removeChild(root);
  });

  it("navigates to /dashboard when Asgard logo is clicked and does not render text dashboard button when open", async () => {
    const app = createApp({
      render() {
        return h(Sidebar, {
          sessions: mockSessions,
          activeSessionId: "sess-1",
          isOpen: true,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const logo = root.querySelector("h1");
    expect(logo).not.toBeNull();
    expect(logo?.textContent?.trim()).toBe("Asgard");
    expect(logo?.getAttribute("title")).toBe("Dashboard");

    logo?.click();
    await nextTick();
    expect(mockPush).toHaveBeenCalledWith("/dashboard");

    const buttons = Array.from(root.querySelectorAll("button"));
    const dashboardBtn = buttons.find((btn) => btn.textContent?.includes("Dashboard"));
    expect(dashboardBtn).toBeUndefined();

    app.unmount();
  });

  it("renders Dashboard button icon-only and with title when sidebar is collapsed", async () => {
    const app = createApp({
      render() {
        return h(Sidebar, {
          sessions: mockSessions,
          activeSessionId: "sess-1",
          isOpen: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const buttons = Array.from(root.querySelectorAll("button"));
    const dashboardBtn = buttons.find((btn) => btn.getAttribute("title") === "Dashboard");
    expect(dashboardBtn).toBeDefined();
    expect(dashboardBtn?.textContent?.trim()).toBe("");

    dashboardBtn?.click();
    await nextTick();
    expect(mockPush).toHaveBeenCalledWith("/dashboard");

    app.unmount();
  });

  it("highlights collapsed Dashboard button when route is /dashboard", async () => {
    mockCurrentRoute = { path: "/dashboard" };

    const app = createApp({
      render() {
        return h(Sidebar, {
          sessions: mockSessions,
          activeSessionId: null,
          isOpen: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const buttons = Array.from(root.querySelectorAll("button"));
    const dashboardBtn = buttons.find((btn) => btn.getAttribute("title") === "Dashboard");
    expect(dashboardBtn).toBeDefined();
    expect(dashboardBtn?.className).toContain("bg-primary/10");
    expect(dashboardBtn?.className).toContain("text-primary");

    app.unmount();
  });

  it("emits archive-session when triggered from SessionList", async () => {
    let archivedId: string | null = null;

    const app = createApp({
      render() {
        return h(Sidebar, {
          sessions: mockSessions,
          activeSessionId: "sess-1",
          isOpen: true,
          "onArchive-session": (id: string) => {
            archivedId = id;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const archiveBtn = root.querySelector('button[title="Archive session"]') as HTMLButtonElement;
    expect(archiveBtn).not.toBeNull();
    archiveBtn.click();
    await nextTick();

    expect(archivedId).toBe("sess-1");

    app.unmount();
  });

  it("renders localized text in Chinese", async () => {
    setLocale("zh-CN", false);

    const app = createApp({
      render() {
        return h(Sidebar, {
          sessions: mockSessions,
          activeSessionId: "sess-1",
          isOpen: true,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const buttons = Array.from(root.querySelectorAll("button"));
    const newChatBtn = buttons.find((btn) => btn.textContent?.includes("新建会话"));
    expect(newChatBtn).toBeDefined();

    const logo = root.querySelector("h1");
    expect(logo?.getAttribute("title")).toBe("看板");

    app.unmount();
  });
});
