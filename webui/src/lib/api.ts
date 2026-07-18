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

// Session API client placeholders for now
const SESSIONS_STORAGE_KEY = "asgard_webui_sessions";

export async function getSessions(): Promise<ChatSession[]> {
  try {
    const res = await fetch("/api/sessions");
    if (res.ok) return await res.json();
  } catch {}

  // Fallback to localStorage
  const data = localStorage.getItem(SESSIONS_STORAGE_KEY);
  return data ? JSON.parse(data) : [];
}

export async function saveSessionToLocal(session: ChatSession): Promise<void> {
  const sessions = await getSessions();
  const idx = sessions.findIndex((s) => s.chatID === session.chatID);
  if (idx > -1) {
    sessions[idx] = session;
  } else {
    sessions.unshift(session);
  }
  localStorage.setItem(SESSIONS_STORAGE_KEY, JSON.stringify(sessions));
}

export async function deleteSessionFromLocal(chatID: string): Promise<void> {
  const sessions = await getSessions();
  const filtered = sessions.filter((s) => s.chatID !== chatID);
  localStorage.setItem(SESSIONS_STORAGE_KEY, JSON.stringify(filtered));
}
