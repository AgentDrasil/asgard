// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import Sidebar from "./Sidebar.vue";
import { i18n, setLocale } from "../i18n";
import { isAuthEnabled, logout } from "../lib/api";
import type { ChatSession } from "../types";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/chat/123" }),
  useRouter: () => ({ push: vi.fn<(path: string) => void>() }),
}));

vi.mock("../lib/api", () => ({
  isAuthEnabled: vi.fn<() => boolean>(() => true),
  logout: vi.fn<() => void>(),
}));

const mockSessions: ChatSession[] = [
  {
    chatID: "sess-1",
    title: "Test Session 1",
    currentAgent: "Coder",
    runDir: "/workspace",
    isRunning: false,
  },
];

function mountSidebar(root: HTMLElement) {
  const app = createApp({
    render() {
      return h(Sidebar, { sessions: mockSessions, activeSessionId: "sess-1", isOpen: true });
    },
  });
  app.use(i18n);
  app.mount(root);
}

describe("Sidebar.vue authentication entry points", () => {
  let root: HTMLElement;

  beforeEach(() => {
    setLocale("en", false);
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    vi.clearAllMocks();
    document.body.removeChild(root);
  });

  it("shows a sign-out action and calls logout when clicked", async () => {
    vi.mocked(isAuthEnabled).mockReturnValue(true);
    mountSidebar(root);
    await nextTick();

    const button = root.querySelector<HTMLButtonElement>('button[title="Sign out"]');
    expect(button).not.toBeNull();

    button!.click();
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("hides the sign-out action when the backend has no auth", async () => {
    vi.mocked(isAuthEnabled).mockReturnValue(false);
    mountSidebar(root);
    await nextTick();

    expect(root.querySelector('button[title="Sign out"]')).toBeNull();
  });
});
