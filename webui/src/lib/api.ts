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

  // Fallback to localStorage
  const sessions = await getSessions();
  return sessions.find((s) => s.chatID === chatID) || null;
}

// Session API client placeholders for now
const SESSIONS_STORAGE_KEY = "asgard_webui_sessions";

export async function getSessions(): Promise<ChatSession[]> {
  try {
    const res = await fetch("/api/sessions");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("Failed to fetch sessions from backend:", err);
  }

  // Fallback to localStorage
  const data = localStorage.getItem(SESSIONS_STORAGE_KEY);
  return data ? JSON.parse(data) : [];
}

export async function saveSessionToLocal(session: ChatSession): Promise<void> {
  try {
    const res = await fetch("/api/sessions", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(session),
    });
    if (res.ok) return;
  } catch (err) {
    console.error("Failed to save session to backend:", err);
  }

  // Fallback to localStorage
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
  try {
    const res = await fetch(`/api/sessions?chat_id=${encodeURIComponent(chatID)}`, {
      method: "DELETE",
    });
    if (res.ok) return;
  } catch (err) {
    console.error("Failed to delete session from backend:", err);
  }

  // Fallback to localStorage
  const sessions = await getSessions();
  const filtered = sessions.filter((s) => s.chatID !== chatID);
  localStorage.setItem(SESSIONS_STORAGE_KEY, JSON.stringify(filtered));
}
