// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, defineComponent, h, nextTick, watchEffect } from "vue";
import { createRouter, createMemoryHistory } from "vue-router";
import App from "./App.vue";
import { i18n, setLocale } from "./i18n";
import type { AgentInfo, ChatSession } from "./types";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

vi.mock("./lib/push", () => ({
  initPushNotifications: vi.fn<() => Promise<null>>().mockResolvedValue(null),
}));

const mocks = vi.hoisted(() => {
  const agents: AgentInfo[] = [
    {
      id: "agent-a",
      name: "Agent A",
      description: "",
      run_dirs: ["/ws/a1", "/ws/a2"],
      main_agent: true,
    } as AgentInfo,
    {
      id: "agent-b",
      name: "Agent B",
      description: "",
      run_dirs: ["/ws/b1", "/ws/b2"],
      main_agent: true,
    } as AgentInfo,
  ];

  const sessions: ChatSession[] = [
    {
      chatID: "sess-a",
      title: "Session A",
      currentAgent: "agent-a",
      runDir: "/ws/a2",
      isRunning: false,
    },
    {
      chatID: "sess-b",
      title: "Session B",
      currentAgent: "agent-b",
      runDir: "/ws/b1",
      isRunning: false,
    },
  ];

  return { agents, sessions };
});

vi.mock("./lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./lib/api")>();
  return {
    ...actual,
    getAgents: vi.fn<() => Promise<AgentInfo[]>>().mockResolvedValue(mocks.agents),
    getSessions: vi.fn<() => Promise<ChatSession[]>>().mockResolvedValue(mocks.sessions),
    getSession: vi
      .fn<(id: string) => Promise<ChatSession | null>>()
      .mockImplementation(async (id: string) => {
        const found = mocks.sessions.find((s) => s.chatID === id);
        return found ? { ...found, messages: [], artifacts: [], queuedMessages: [] } : null;
      }),
    getDirInfo: vi
      .fn<(dir: string) => Promise<{ subdirs: string[]; gitRoot: string }>>()
      .mockResolvedValue({ subdirs: [], gitRoot: "" }),
    getSystemStatus: vi.fn<() => Promise<null>>().mockResolvedValue(null),
    getKeybindings: vi
      .fn<() => Promise<{ overrides: Record<string, unknown> }>>()
      .mockResolvedValue({ overrides: {} }),
    reloadAgents: vi.fn<() => Promise<{ success: boolean }>>().mockResolvedValue({ success: true }),
  };
});

class MockEventSource {
  addEventListener = vi.fn<() => void>();
  close = vi.fn<() => void>();
  onopen: (() => void) | null = null;
  onerror: ((err: Event) => void) | null = null;
}

describe("App new chat from an active session", () => {
  let root: HTMLElement;
  let captured: { agent?: string; dir?: string };
  let storageMock: Storage;

  const createStorageMock = (): Storage => {
    let store: Record<string, string> = {};
    return {
      get length() {
        return Object.keys(store).length;
      },
      clear: () => {
        store = {};
      },
      getItem: (key: string) => (key in store ? store[key] : null),
      key: (index: number) => Object.keys(store)[index] ?? null,
      removeItem: (key: string) => {
        delete store[key];
      },
      setItem: (key: string, value: string) => {
        store[key] = value;
      },
    };
  };

  const NewChatStub = defineComponent({
    name: "NewChatStub",
    props: ["selectedAgentId", "selectedDir"],
    setup(props) {
      watchEffect(() => {
        captured.agent = props.selectedAgentId;
        captured.dir = props.selectedDir;
      });
      return () => h("div", { class: "newchat-stub" });
    },
  });

  const BlankStub = defineComponent({
    name: "BlankStub",
    setup: () => () => h("div"),
  });

  beforeEach(() => {
    captured = {};
    setLocale("en", false);
    storageMock = createStorageMock();
    vi.stubGlobal("localStorage", storageMock);
    storageMock.setItem("asgard_sidebar_view_mode", "agent");
    vi.stubGlobal("EventSource", MockEventSource);
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    if (root.parentNode) {
      root.parentNode.removeChild(root);
    }
  });

  it("keeps the clicked agent and workspace after navigating from a chat to /newchat", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/newchat", component: NewChatStub },
        { path: "/chat/:id", component: BlankStub },
        { path: "/dashboard", component: BlankStub },
      ],
    });

    // Open a session belonging to Agent B...
    await router.push("/chat/sess-b");

    const app = createApp(App);
    app.use(i18n);
    app.use(router);
    app.mount(root);
    await router.isReady();
    await nextTick();
    // allow onMounted loadAgents/loadSessions and the route watcher to settle
    await new Promise((r) => setTimeout(r, 0));
    await nextTick();

    // ...then start a new chat from Agent A's "/ws/a2" workspace group.
    const plusBtn = root.querySelector(
      'div[title="/ws/a2"] button[title="New chat with this agent and workspace"]',
    ) as HTMLButtonElement | null;
    expect(plusBtn).not.toBeNull();
    plusBtn!.click();

    await router.isReady();
    await nextTick();
    await new Promise((r) => setTimeout(r, 0));
    await nextTick();

    expect(router.currentRoute.value.path).toBe("/newchat");
    expect(captured.agent).toBe("agent-a");
    expect(captured.dir).toBe("/ws/a2");

    app.unmount();
  });
});
