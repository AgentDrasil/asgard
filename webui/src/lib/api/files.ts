import type { FileScope, FileSearchResult, FileTreeEntry, WorkspaceFileContent } from "../../types";
import { appendAccessToken } from "./auth";
import { apiFetch } from "./client";

export async function getFileTree(sessionId: string, subPath = ""): Promise<FileTreeEntry[]> {
  if (!sessionId) return [];
  try {
    let url = `/api/files/tree?session_id=${encodeURIComponent(sessionId)}`;
    if (subPath) url += `&path=${encodeURIComponent(subPath)}`;
    const res = await apiFetch(url);
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error(body?.error || `Failed to load file tree (${res.status})`);
    }
    const data = await res.json();
    return data.entries || [];
  } catch (err) {
    console.error("getFileTree error:", err);
    throw err;
  }
}

export async function getFileContent(
  sessionId: string,
  path: string,
  scope?: FileScope,
): Promise<WorkspaceFileContent | null> {
  if (!sessionId || !path) return null;
  try {
    const scopeParam = scope ? `&scope=${encodeURIComponent(scope)}` : "";
    const res = await apiFetch(
      `/api/files/content?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}${scopeParam}`,
    );
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      throw new Error(body?.error || `Failed to load file (${res.status})`);
    }
    return await res.json();
  } catch (err) {
    console.error("getFileContent error:", err);
    throw err;
  }
}

export async function searchFiles(
  sessionId: string,
  query: string,
  limit = 50,
  signal?: AbortSignal,
): Promise<FileSearchResult[]> {
  if (!sessionId) return [];
  try {
    const res = await apiFetch(
      `/api/files/search?session_id=${encodeURIComponent(sessionId)}&query=${encodeURIComponent(query)}&limit=${limit}`,
      { signal },
    );
    if (!res.ok) return [];
    const data = await res.json();
    return data.files || [];
  } catch (err: any) {
    if (err?.name !== "AbortError") {
      console.error("searchFiles error:", err);
    }
    return [];
  }
}

export function getRawFileContentUrl(sessionId: string, path: string): string {
  return appendAccessToken(
    `/api/files/content?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}&raw=1`,
  );
}

// getRawWorkspaceFileUrl returns a URL usable directly as an <img>/<iframe> src,
// so the bearer token travels in the query string (headers are not available on
// resource loads). Note the token is a snapshot: a refresh will not update an
// URL that is already rendered.
export function getRawWorkspaceFileUrl(sessionId: string, path: string): string {
  return appendAccessToken(
    `/api/v1/workspace/file?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}&raw=1`,
  );
}
