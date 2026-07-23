import type { AgentInfo, ChatSession } from "../types";

// Centralized fetch wrapper that handles 401 Unauthorized by redirecting for SSO refresh
export async function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const response = await fetch(input, init);
  if (response.status === 401) {
    console.log("apiFetch: 401 received, redirecting to refresh session via SSO...");
    const url = new URL(window.location.href);
    url.searchParams.set("_auth_refresh", Date.now().toString());
    window.location.href = url.toString();
  }
  return response;
}

// Fetch loaded agents from backend
export async function getAgents(): Promise<AgentInfo[]> {
  try {
    const res = await apiFetch("/api/agents");
    if (!res.ok) throw new Error("Failed to fetch agents");
    return await res.json();
  } catch (err) {
    console.error("getAgents error, fallback to mock:", err);
    return [
      {
        id: "agent_father",
        name: "Agent Father",
        description: "Default coding agent orchestrator",
        run_dirs: ["/home/user/src/AgentDrasil/asgard"],
      },
    ];
  }
}

export async function getSession(chatID: string): Promise<ChatSession | null> {
  try {
    const res = await apiFetch(`/api/sessions/${encodeURIComponent(chatID)}`);
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch session from backend:", err);
  }
  return null;
}

export async function getSessions(): Promise<ChatSession[]> {
  try {
    const res = await apiFetch("/api/sessions");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch sessions from backend:", err);
  }
  return [];
}

export async function deleteSessionFromLocal(chatID: string): Promise<void> {
  try {
    await apiFetch(`/api/sessions?chat_id=${encodeURIComponent(chatID)}`, {
      method: "DELETE",
    });
  } catch (err) {
    console.error("Failed to delete session from backend:", err);
  }
}
