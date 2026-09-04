// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import ChatArea from "./ChatArea.vue";
import type { AgentInfo, ChatMessage, QueuedMessage } from "../types";
import { i18n } from "../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock api
vi.mock("../lib/api", () => ({
  getDirInfo: vi
    .fn<(...args: any[]) => Promise<any>>()
    .mockResolvedValue({ gitRoot: "/home/user/project" }),
  uploadAttachment: vi.fn<(...args: any[]) => Promise<any>>(),
  getFileContent: vi.fn<(...args: any[]) => Promise<any>>(),
}));

describe("ChatArea.vue", () => {
  let root: HTMLElement;
  let originalLocalStorage: Storage;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();

    // Mock HTMLDivElement.prototype.scrollTo for jsdom
    if (!HTMLDivElement.prototype.scrollTo) {
      HTMLDivElement.prototype.scrollTo = vi.fn<(...args: any[]) => void>();
    } else {
      vi.spyOn(HTMLDivElement.prototype, "scrollTo").mockImplementation(() => {});
    }

    // Mock localStorage if missing or undefined
    const mockStorage: Record<string, string> = {};
    const storageMock: Storage = {
      getItem: (key: string) => mockStorage[key] || null,
      setItem: (key: string, value: string) => {
        mockStorage[key] = value;
      },
      removeItem: (key: string) => {
        delete mockStorage[key];
      },
      clear: () => {
        Object.keys(mockStorage).forEach((k) => delete mockStorage[k]);
      },
      key: (index: number) => Object.keys(mockStorage)[index] || null,
      length: 0,
    };
    originalLocalStorage = window.localStorage;
    Object.defineProperty(window, "localStorage", {
      value: storageMock,
      configurable: true,
      writable: true,
    });
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
    if (originalLocalStorage) {
      Object.defineProperty(window, "localStorage", {
        value: originalLocalStorage,
        configurable: true,
        writable: true,
      });
    }
  });

  const dummyAgent: AgentInfo = {
    id: "agent-1",
    name: "Coding Agent",
    description: "AI coding assistant",
    run_dirs: ["/home/user/project"],
    type: "coding",
    icon: "fluent-color:bot-24",
  };

  const dummyMessages: ChatMessage[] = [
    {
      id: "msg-1",
      role: "user",
      content: "Hello world message for search",
      timestamp: 1725120000000,
    },
    {
      id: "msg-2",
      role: "assistant",
      content: "Here is the response text",
      timestamp: 1725120001000,
    },
  ];

  it("renders with messages and workspace info", async () => {
    const app = createApp({
      render() {
        return h(ChatArea, {
          messages: dummyMessages,
          loading: false,
          activeAgent: dummyAgent,
          runDir: "/home/user/project",
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    expect(root.textContent).toContain("Coding Agent");
    expect(root.textContent).toContain("Hello world message for search");
    expect(root.textContent).toContain("Here is the response text");

    // Initially find bar is not rendered
    expect(root.querySelector(".find-bar-ignore")).toBeNull();

    app.unmount();
  });

  it("opens find bar on Ctrl+F even when an input or textarea is focused", async () => {
    const app = createApp({
      render() {
        return h(ChatArea, {
          messages: dummyMessages,
          loading: false,
          activeAgent: dummyAgent,
          runDir: "/home/user/project",
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    // Verify initial find bar state is closed
    expect(root.querySelector(".find-bar-ignore")).toBeNull();

    // Simulate an external / embedded input element with focus
    const input = document.createElement("textarea");
    root.appendChild(input);
    input.focus();
    expect(document.activeElement).toBe(input);

    // Dispatch Ctrl+F keydown on the focused element with bubble
    const event = new KeyboardEvent("keydown", {
      ctrlKey: true,
      key: "f",
      code: "KeyF",
      bubbles: true,
      cancelable: true,
    });
    const preventSpy = vi.spyOn(event, "preventDefault");
    const stopSpy = vi.spyOn(event, "stopPropagation");

    input.dispatchEvent(event);
    await nextTick();

    // FindBar should now be rendered and open
    const findBar = root.querySelector(".find-bar-ignore");
    expect(findBar).not.toBeNull();
    expect(findBar?.querySelector("input[placeholder='Find in page...']")).not.toBeNull();
    expect(preventSpy).toHaveBeenCalled();
    expect(stopSpy).toHaveBeenCalled();

    app.unmount();
  });

  it("does not open find bar on non-matching shortcuts like Ctrl+Alt+F", async () => {
    const app = createApp({
      render() {
        return h(ChatArea, {
          messages: dummyMessages,
          loading: false,
          activeAgent: dummyAgent,
          runDir: "/home/user/project",
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    expect(root.querySelector(".find-bar-ignore")).toBeNull();

    const event = new KeyboardEvent("keydown", {
      ctrlKey: true,
      altKey: true,
      key: "f",
      code: "KeyF",
      bubbles: true,
      cancelable: true,
    });

    window.dispatchEvent(event);
    await nextTick();

    expect(root.querySelector(".find-bar-ignore")).toBeNull();

    app.unmount();
  });

  it("toggles find bar when clicking Find button in header", async () => {
    const app = createApp({
      render() {
        return h(ChatArea, {
          messages: dummyMessages,
          loading: false,
          activeAgent: dummyAgent,
          runDir: "/home/user/project",
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const findBtn = root.querySelector('button[title^="Find in chat"]') as HTMLButtonElement;
    expect(findBtn).not.toBeNull();

    // Open via button
    findBtn.click();
    await nextTick();

    const findBar = root.querySelector(".find-bar-ignore");
    expect(findBar).not.toBeNull();

    // Find button has active class
    expect(findBtn.classList.contains("btn-primary")).toBe(true);

    app.unmount();
  });

  it("renders queued messages in chat stream and emits edit-queued and delete-queued events", async () => {
    const dummyQueued: QueuedMessage[] = [
      {
        id: "qmsg-1",
        chatId: "sess-123",
        prompt: "First queued task",
        createdAt: "2026-09-03T20:00:00Z",
        updatedAt: "2026-09-03T20:00:00Z",
      },
      {
        id: "qmsg-2",
        chatId: "sess-123",
        prompt: "Second queued task",
        createdAt: "2026-09-03T20:01:00Z",
        updatedAt: "2026-09-03T20:01:00Z",
      },
    ];

    let editedId = "";
    let editedText = "";
    let deletedId = "";

    const app = createApp({
      render() {
        return h(ChatArea, {
          messages: dummyMessages,
          loading: false,
          activeAgent: dummyAgent,
          runDir: "/home/user/project",
          sessionId: "sess-123",
          queuedMessages: dummyQueued,
          onEditQueued: (id: string, text: string) => {
            editedId = id;
            editedText = text;
          },
          onDeleteQueued: (id: string) => {
            deletedId = id;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const queueContainer = root.querySelector('[data-testid="queued-messages-container"]');
    expect(queueContainer).not.toBeNull();

    const cards = root.querySelectorAll('[data-testid="queued-message-card"]');
    expect(cards.length).toBe(2);
    expect(cards[0].textContent).toContain("First queued task");
    expect(cards[1].textContent).toContain("Second queued task");

    // Click delete on first card
    const deleteBtn = cards[0].querySelector(
      '[data-testid="delete-queued-button"]',
    ) as HTMLButtonElement;
    expect(deleteBtn).not.toBeNull();
    deleteBtn.click();
    await nextTick();
    expect(deletedId).toBe("qmsg-1");

    // Click edit on second card
    const editBtn = cards[1].querySelector(
      '[data-testid="edit-queued-button"]',
    ) as HTMLButtonElement;
    expect(editBtn).not.toBeNull();
    editBtn.click();
    await nextTick();

    const textarea = cards[1].querySelector(
      '[data-testid="queued-edit-textarea"]',
    ) as HTMLTextAreaElement;
    expect(textarea).not.toBeNull();
    textarea.value = "Updated second queued task";
    textarea.dispatchEvent(new Event("input"));
    await nextTick();

    const saveBtn = cards[1].querySelector('[data-testid="save-edit-button"]') as HTMLButtonElement;
    expect(saveBtn).not.toBeNull();
    saveBtn.click();
    await nextTick();

    expect(editedId).toBe("qmsg-2");
    expect(editedText).toBe("Updated second queued task");

    app.unmount();
  });
});
