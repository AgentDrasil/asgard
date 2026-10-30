import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";
import { useSessions } from "./useSessions";
import type { AgentInfo } from "../types";

describe("useSessions handleNewChat", () => {
  it("resets selectedDir to current agent default runDir when runDir is omitted", () => {
    const route = { params: {}, path: "/chat/old-id" } as any;
    const router = { push: vi.fn<(url: string) => void>() } as any;
    const agents = ref<AgentInfo[]>([
      {
        id: "agent-1",
        name: "Agent One",
        run_dirs: ["/home/user/project"],
      } as AgentInfo,
    ]);
    const selectedAgentId = ref("agent-1");
    const selectedDir = ref("/home/user/tmp/old-session-id");
    const activeSessionId = ref<string | null>("old-session-id");
    const loadSessions = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);

    const { handleNewChat } = useSessions(
      route,
      router,
      agents,
      selectedAgentId,
      selectedDir,
      activeSessionId,
      loadSessions,
    );

    // Call handleNewChat without explicit runDir (clicking "New Chat")
    handleNewChat();

    expect(selectedDir.value).toBe("/home/user/project");
    expect(router.push).toHaveBeenCalledWith("/newchat");
  });

  it("sets selectedDir to explicitly provided runDir", () => {
    const route = { params: {}, path: "/chat/old-id" } as any;
    const router = { push: vi.fn<(url: string) => void>() } as any;
    const agents = ref<AgentInfo[]>([
      {
        id: "agent-1",
        name: "Agent One",
        run_dirs: ["/home/user/project"],
      } as AgentInfo,
    ]);
    const selectedAgentId = ref("agent-1");
    const selectedDir = ref("/home/user/tmp/old-session-id");
    const activeSessionId = ref<string | null>("old-session-id");
    const loadSessions = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);

    const { handleNewChat } = useSessions(
      route,
      router,
      agents,
      selectedAgentId,
      selectedDir,
      activeSessionId,
      loadSessions,
    );

    handleNewChat(undefined, undefined, "/custom/dir");

    expect(selectedDir.value).toBe("/custom/dir");
    expect(router.push).toHaveBeenCalledWith("/newchat");
  });

  it("switches agent and resets selectedDir to new agent default runDir", () => {
    const route = { params: {}, path: "/newchat" } as any;
    const router = { push: vi.fn<(url: string) => void>() } as any;
    const agents = ref<AgentInfo[]>([
      {
        id: "agent-1",
        name: "Agent One",
        run_dirs: ["/home/user/project1"],
      } as AgentInfo,
      {
        id: "agent-2",
        name: "Agent Two",
        run_dirs: ["/home/user/project2"],
      } as AgentInfo,
    ]);
    const selectedAgentId = ref("agent-1");
    const selectedDir = ref("/home/user/tmp/old-session-id");
    const activeSessionId = ref<string | null>(null);
    const loadSessions = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);

    const { handleNewChat } = useSessions(
      route,
      router,
      agents,
      selectedAgentId,
      selectedDir,
      activeSessionId,
      loadSessions,
    );

    handleNewChat(undefined, "agent-2");

    expect(selectedAgentId.value).toBe("agent-2");
    expect(selectedDir.value).toBe("/home/user/project2");
  });

  describe("handleUnlockSession", () => {
    it("returns false for empty session id", async () => {
      const route = { params: {}, path: "/chat/123" } as any;
      const router = { push: vi.fn<(url: string) => void>() } as any;
      const agents = ref<AgentInfo[]>([]);
      const selectedAgentId = ref("");
      const selectedDir = ref("");
      const activeSessionId = ref<string | null>("123");
      const loadSessions = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);

      const { handleUnlockSession } = useSessions(
        route,
        router,
        agents,
        selectedAgentId,
        selectedDir,
        activeSessionId,
        loadSessions,
      );

      const res = await handleUnlockSession("");
      expect(res).toBe(false);
    });

    it("calls unlockSessionById when provided and returns true on success", async () => {
      const route = { params: {}, path: "/chat/123" } as any;
      const router = { push: vi.fn<(url: string) => void>() } as any;
      const agents = ref<AgentInfo[]>([]);
      const selectedAgentId = ref("");
      const selectedDir = ref("");
      const activeSessionId = ref<string | null>("123");
      const loadSessions = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
      const unlockSessionById = vi.fn<(id: string) => Promise<boolean>>().mockResolvedValue(true);

      const { handleUnlockSession } = useSessions(
        route,
        router,
        agents,
        selectedAgentId,
        selectedDir,
        activeSessionId,
        loadSessions,
        undefined,
        unlockSessionById,
      );

      const res = await handleUnlockSession("123");
      expect(res).toBe(true);
      expect(unlockSessionById).toHaveBeenCalledWith("123");
    });

    it("tracks isUnlockingSession and prevents concurrent unlock requests", async () => {
      const route = { params: {}, path: "/chat/123" } as any;
      const router = { push: vi.fn<(url: string) => void>() } as any;
      const agents = ref<AgentInfo[]>([]);
      const selectedAgentId = ref("");
      const selectedDir = ref("");
      const activeSessionId = ref<string | null>("123");
      const loadSessions = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);

      let resolveUnlock: (val: boolean) => void = () => {};
      const unlockPromise = new Promise<boolean>((resolve) => {
        resolveUnlock = resolve;
      });
      const unlockSessionById = vi
        .fn<(id: string) => Promise<boolean>>()
        .mockReturnValue(unlockPromise);

      const { handleUnlockSession, isUnlockingSession } = useSessions(
        route,
        router,
        agents,
        selectedAgentId,
        selectedDir,
        activeSessionId,
        loadSessions,
        undefined,
        unlockSessionById,
      );

      expect(isUnlockingSession.value).toBe(false);
      const call1 = handleUnlockSession("123");
      expect(isUnlockingSession.value).toBe(true);

      // Concurrent call should immediately return false without calling unlockSessionById again
      const call2 = await handleUnlockSession("123");
      expect(call2).toBe(false);
      expect(unlockSessionById).toHaveBeenCalledTimes(1);

      resolveUnlock(true);
      const res1 = await call1;
      expect(res1).toBe(true);
      expect(isUnlockingSession.value).toBe(false);
    });
  });
});
