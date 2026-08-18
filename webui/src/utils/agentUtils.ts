import type { AgentInfo } from "../types";

/**
 * Returns the icon for an agent given its name/id and a list of available agents.
 * Falls back to activeAgent icon or default bot icon.
 */
export function getAgentIcon(
  agentName?: string,
  agents?: AgentInfo[],
  activeAgent?: AgentInfo | null,
): string {
  if (agentName && agents) {
    const matched = agents.find((a) => a.name === agentName || a.id === agentName);
    if (matched?.icon) return matched.icon;
  }
  return activeAgent?.icon || "fluent-color:bot-24";
}

/**
 * Replaces home directory prefix with ~ for cleaner path display.
 */
export function formatPath(path?: string): string {
  if (!path) return "";
  return path.replace(/^\/home\/[^/]+/, "~");
}
