import { describe, it, expect } from "vitest";
import { getAgentIcon, formatPath } from "./agentUtils";
import type { AgentInfo } from "../types";

describe("agentUtils", () => {
  const mockAgents: AgentInfo[] = [
    {
      id: "agent-a",
      name: "Alpha Agent",
      type: "agent",
      description: "Alpha",
      icon: "solar:atom-bold",
      run_dirs: [],
      main_agent: true,
      models: [],
    },
  ];

  it("prioritizes fallbackIcon if provided", () => {
    expect(getAgentIcon("agent-a", mockAgents, null, "custom:saved-icon")).toBe(
      "custom:saved-icon",
    );
  });

  it("resolves agent icon from agents list by id or name", () => {
    expect(getAgentIcon("agent-a", mockAgents)).toBe("solar:atom-bold");
    expect(getAgentIcon("Alpha Agent", mockAgents)).toBe("solar:atom-bold");
  });

  it("falls back to activeAgent icon when not found in list", () => {
    const active = {
      id: "active",
      name: "Active",
      type: "agent",
      description: "",
      icon: "solar:user-bold",
      run_dirs: [],
      main_agent: true,
      models: [],
    };
    expect(getAgentIcon("unknown", mockAgents, active)).toBe("solar:user-bold");
  });

  it("falls back to default bot icon when no match and no activeAgent", () => {
    expect(getAgentIcon("unknown", mockAgents)).toBe("fluent-color:bot-24");
    expect(getAgentIcon()).toBe("fluent-color:bot-24");
  });

  it("formats home path correctly", () => {
    expect(formatPath("/home/user/project")).toBe("~/project");
    expect(formatPath("/var/log")).toBe("/var/log");
    expect(formatPath("")).toBe("");
  });
});
