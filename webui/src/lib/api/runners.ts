import type { RunnersResponse, ManageRunnersResponse } from "../../types";
import { apiFetch } from "./client";

// withRunner appends the ?runner= query parameter the backend uses to scope a
// request that names no chat (agents list, subdirs, sidebar terminal) to one
// runner. An empty runner (legacy mode) leaves the URL untouched.
export function withRunner(url: string, runner?: string): string {
  if (!runner) return url;
  return `${url}${url.includes("?") ? "&" : "?"}runner=${encodeURIComponent(runner)}`;
}

// RunnerRequestError is a runner binding failure on a chat-creating request:
// 409 (chat bound to another runner) or 503 (runner offline).
export class RunnerRequestError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "RunnerRequestError";
    this.status = status;
  }
}

// Fetch the runners known to the backend. Returns null on failure.
// Admin only: all runners with their assigned users, plus known users.
export async function getManageRunners(): Promise<ManageRunnersResponse | null> {
  try {
    const res = await apiFetch("/api/manage/runners");
    if (!res.ok) return null;
    const data = await res.json();
    return {
      runners: Array.isArray(data?.runners) ? data.runners : [],
      users: Array.isArray(data?.users) ? data.users : [],
    };
  } catch (err) {
    console.error("getManageRunners error:", err);
    return null;
  }
}

// Admin only: replace the set of users assigned to a runner.
export async function setRunnerUsers(nodeId: string, users: string[]): Promise<string | null> {
  try {
    const res = await apiFetch(`/api/manage/runners/${encodeURIComponent(nodeId)}/users`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ users }),
    });
    if (res.ok) return null;
    const data = await res.json().catch(() => null);
    return data?.error || `HTTP ${res.status}`;
  } catch (err) {
    return String(err);
  }
}

export async function getRunners(): Promise<RunnersResponse | null> {
  try {
    const res = await apiFetch("/api/runners");
    if (!res.ok) return null;
    const data = await res.json();
    return {
      backend_version: data?.backend_version || "",
      runners: Array.isArray(data?.runners) ? data.runners : [],
      runner_mode: data?.runner_mode,
    };
  } catch (err) {
    console.error("getRunners error:", err);
    return null;
  }
}
