import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";
import { useSessions } from "./useSessions";
import type { AgentInfo } from "../types";

describe("useSessions handleNewChat", () => {
  it("resets selectedDir to current agent default runDir when runDir is omitted", () => {
    const route = { params: {}, path: "/chat/old-id" } as any;
    const router = { push: vi.fn() } as any;
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
    const loadSessions = vi.fn().mockResolvedValue(undefined);

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
    const router = { push: vi.fn() } as any;
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
    const loadSessions = vi.fn().mockResolvedValue(undefined);

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
    const router = { push: vi.fn() } as any;
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
    const loadSessions = vi.fn().mockResolvedValue(undefined);

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
});
