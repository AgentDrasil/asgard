// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { createApp, h, nextTick } from "vue";
import DashboardView from "./DashboardView.vue";
import * as api from "../lib/api";
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
vi.mock("vue-router", () => ({
  useRouter: () => ({
    push: mockPush,
  }),
}));

describe("DashboardView.vue", () => {
  let root: HTMLElement;

  const now = Date.now();
  const mockActiveSessions: ChatSession[] = [
    {
      chatID: "sess-running-1",
      title: "Running Task 1",
      currentAgent: "coder",
      runDir: "/workspace/proj",
      isRunning: true,
      isWaitingForUser: false,
      isArchived: false,
      updatedAt: new Date(now - 10 * 60 * 1000).toISOString(),
    },
    {
      chatID: "sess-waiting-1",
      title: "Waiting For Feedback",
      currentAgent: "architect",
      runDir: "/workspace/proj",
      isRunning: false,
      isWaitingForUser: true,
      isArchived: false,
      updatedAt: new Date(now - 30 * 60 * 1000).toISOString(),
    },
    {
      chatID: "sess-recent-1",
      title: "Recently Done Task",
      currentAgent: "tester",
      runDir: "/workspace/proj",
      isRunning: false,
      isWaitingForUser: false,
      isArchived: false,
      updatedAt: new Date(now - 60 * 60 * 1000).toISOString(), // 1 hour ago (< 3h)
    },
    {
      chatID: "sess-old-1",
      title: "Old Completed Task",
      currentAgent: "coder",
      runDir: "/workspace/proj",
      isRunning: false,
      isWaitingForUser: false,
      isArchived: false,
      updatedAt: new Date(now - 5 * 3600 * 1000).toISOString(), // 5 hours ago (> 3h)
    },
  ];

  const mockArchivedSessions: ChatSession[] = [
    {
      chatID: "sess-archived-1",
      title: "Archived Project Alpha",
      currentAgent: "coder",
      runDir: "/workspace/old",
      isRunning: false,
      isWaitingForUser: false,
      isArchived: true,
      updatedAt: new Date(now - 24 * 3600 * 1000).toISOString(),
    },
  ];

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    mockPush.mockReset();

    vi.spyOn(api, "getSessions").mockImplementation(async (archived = false) => {
      if (archived) return [...mockArchivedSessions];
      return [...mockActiveSessions];
    });

    vi.spyOn(api, "archiveSession").mockResolvedValue(true);
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

  it("renders 3-column kanban board and classifies sessions correctly", async () => {
    const app = createApp({
      render() {
        return h(DashboardView);
      },
    });
    app.mount(root);
    await flush();

    // 1. Running column
    const runningCards = root.querySelectorAll('[data-test="session-card-running"]');
    expect(runningCards.length).toBe(1);
    expect(runningCards[0].textContent).toContain("Running Task 1");

    // 2. Waiting column
    const waitingCards = root.querySelectorAll('[data-test="session-card-waiting"]');
    expect(waitingCards.length).toBe(1);
    expect(waitingCards[0].textContent).toContain("Waiting For Feedback");

    // 3. Recently Completed column (< 3h)
    const completedCards = root.querySelectorAll('[data-test="session-card-completed"]');
    expect(completedCards.length).toBe(1);
    expect(completedCards[0].textContent).toContain("Recently Done Task");

    // Old completed task (> 3h) should not appear in any column
    expect(root.textContent).not.toContain("Old Completed Task");

    app.unmount();
  });

  it("navigates to /chat/:id when session card is clicked", async () => {
    const app = createApp({
      render() {
        return h(DashboardView);
      },
    });
    app.mount(root);
    await flush();

    const runningCard = root.querySelector('[data-test="session-card-running"]') as HTMLElement;
    expect(runningCard).not.toBeNull();
    runningCard.click();

    expect(mockPush).toHaveBeenCalledWith("/chat/sess-running-1");

    app.unmount();
  });

  it("archives session on archive button click without triggering router navigation", async () => {
    const archiveSpy = vi.spyOn(api, "archiveSession").mockResolvedValue(true);

    const app = createApp({
      render() {
        return h(DashboardView);
      },
    });
    app.mount(root);
    await flush();

    const archiveBtn = root.querySelector(
      '[data-test="session-card-running"] [data-test="btn-archive"]',
    ) as HTMLButtonElement;
    expect(archiveBtn).not.toBeNull();

    archiveBtn.click();
    await flush();

    expect(archiveSpy).toHaveBeenCalledWith("sess-running-1");
    // Card navigation should NOT have been triggered
    expect(mockPush).not.toHaveBeenCalled();

    // Session should be removed from running cards
    const runningCards = root.querySelectorAll('[data-test="session-card-running"]');
    expect(runningCards.length).toBe(0);

    app.unmount();
  });

  it("filters sessions in kanban view by search query", async () => {
    const app = createApp({
      render() {
        return h(DashboardView);
      },
    });
    app.mount(root);
    await flush();

    const searchInput = root.querySelector('[data-test="search-input"]') as HTMLInputElement;
    expect(searchInput).not.toBeNull();

    // Search for "Feedback"
    searchInput.value = "Feedback";
    searchInput.dispatchEvent(new Event("input"));
    await flush();

    expect(root.querySelectorAll('[data-test="session-card-running"]').length).toBe(0);
    expect(root.querySelectorAll('[data-test="session-card-waiting"]').length).toBe(1);
    expect(root.querySelectorAll('[data-test="session-card-completed"]').length).toBe(0);

    app.unmount();
  });

  it("switches to archived view and renders archived sessions", async () => {
    const app = createApp({
      render() {
        return h(DashboardView);
      },
    });
    app.mount(root);
    await flush();

    const archivedTab = root.querySelector('[data-test="tab-archived"]') as HTMLButtonElement;
    expect(archivedTab).not.toBeNull();
    archivedTab.click();
    await flush();

    // Archived view is active
    expect(root.querySelector('[data-test="archived-view"]')).not.toBeNull();
    const archivedCards = root.querySelectorAll('[data-test="session-card-archived"]');
    expect(archivedCards.length).toBe(1);
    expect(archivedCards[0].textContent).toContain("Archived Project Alpha");

    // Click archived card to navigate
    (archivedCards[0] as HTMLElement).click();
    expect(mockPush).toHaveBeenCalledWith("/chat/sess-archived-1");

    app.unmount();
  });

  it("shows empty state in archived view when there are no archived sessions", async () => {
    vi.spyOn(api, "getSessions").mockImplementation(async (archived = false) => {
      if (archived) return [];
      return [...mockActiveSessions];
    });

    const app = createApp({
      render() {
        return h(DashboardView);
      },
    });
    app.mount(root);
    await flush();

    const archivedTab = root.querySelector('[data-test="tab-archived"]') as HTMLButtonElement;
    archivedTab.click();
    await flush();

    expect(root.querySelector('[data-test="empty-archived"]')).not.toBeNull();
    expect(root.textContent).toContain("No archived sessions");

    app.unmount();
  });
});
