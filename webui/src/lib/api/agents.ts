import type { AgentInfo } from "../../types";
import { apiFetch } from "./client";
import { withRunner } from "./runners";

// Fetch loaded agents from backend
export async function getAgents(runner?: string): Promise<AgentInfo[]> {
  try {
    const res = await apiFetch(withRunner("/api/agents", runner));
    if (!res.ok) throw new Error("Failed to fetch agents");
    return await res.json();
  } catch (err) {
    console.error("getAgents error:", err);
    return [];
  }
}
