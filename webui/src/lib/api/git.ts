import type { DirInfo, GitActionResult, GitDiffFile, GitLogResponse } from "../../types";
import { apiFetch } from "./client";
import { withRunner } from "./runners";

export async function getDirInfo(dir: string, runner?: string): Promise<DirInfo> {
  if (!dir) return { subdirs: [], gitRoot: "" };
  try {
    const res = await apiFetch(withRunner(`/api/subdirs?dir=${encodeURIComponent(dir)}`, runner));
    if (!res.ok) return { subdirs: [], gitRoot: "" };
    const data = await res.json();
    return {
      subdirs: data.subdirs || [],
      gitRoot: data.git_root || data.gitRoot || "",
    };
  } catch (err) {
    console.error("getDirInfo error:", err);
    return { subdirs: [], gitRoot: "" };
  }
}

export async function getSubdirs(dir: string, runner?: string): Promise<string[]> {
  const info = await getDirInfo(dir, runner);
  return info.subdirs;
}

export async function getGitDiff(sessionId: string, commit?: string): Promise<GitDiffFile[]> {
  if (!sessionId) return [];
  const params = new URLSearchParams();
  params.set("session_id", sessionId);

  if (commit) {
    params.set("commit", commit);
  }

  try {
    const res = await apiFetch(`/api/git/diff?${params.toString()}`);
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      throw new Error(data?.error || `Failed to load git diff (${res.status})`);
    }
    const data = await res.json();
    return data.files || [];
  } catch (err) {
    console.error("getGitDiff error:", err);
    throw err;
  }
}

export async function getGitLog(sessionId: string, limit = 10): Promise<GitLogResponse | null> {
  if (!sessionId) return null;
  try {
    const res = await apiFetch(
      `/api/git/log?session_id=${encodeURIComponent(sessionId)}&limit=${limit}`,
    );
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      throw new Error(data?.error || `Failed to load git log (${res.status})`);
    }
    return await res.json();
  } catch (err) {
    console.error("getGitLog error:", err);
    throw err;
  }
}

export async function gitPush(sessionId: string): Promise<GitActionResult> {
  if (!sessionId) return { success: false, error: "Session ID is required" };
  try {
    const res = await apiFetch("/api/git/push", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sessionId }),
    });
    const data = await res.json();
    return {
      success: res.ok && data.success,
      output: data.output,
      error: data.error,
    };
  } catch (err: any) {
    console.error("gitPush error:", err);
    return { success: false, error: err?.message || "Failed to push" };
  }
}

export async function gitPull(sessionId: string): Promise<GitActionResult> {
  if (!sessionId) return { success: false, error: "Session ID is required" };
  try {
    const res = await apiFetch("/api/git/pull", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sessionId }),
    });
    const data = await res.json();
    return {
      success: res.ok && data.success,
      output: data.output,
      error: data.error,
    };
  } catch (err: any) {
    console.error("gitPull error:", err);
    return { success: false, error: err?.message || "Failed to pull" };
  }
}
