import type { AgentInfo, ChatSession } from "../types";

// Fetch loaded agents from backend
export async function getAgents(): Promise<AgentInfo[]> {
  try {
    const res = await fetch("/api/agents");
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
    const res = await fetch(`/api/sessions/${encodeURIComponent(chatID)}`);
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch session from backend:", err);
  }
  return null;
}

export async function getSessions(): Promise<ChatSession[]> {
  try {
    const res = await fetch("/api/sessions");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch sessions from backend:", err);
  }
  return [];
}

export async function saveSessionToLocal(session: ChatSession): Promise<void> {
  try {
    await fetch("/api/sessions", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(session),
    });
  } catch (err) {
    console.error("Failed to save session to backend:", err);
  }
}

export async function deleteSessionFromLocal(chatID: string): Promise<void> {
  try {
    await fetch(`/api/sessions?chat_id=${encodeURIComponent(chatID)}`, {
      method: "DELETE",
    });
  } catch (err) {
    console.error("Failed to delete session from backend:", err);
  }
}
