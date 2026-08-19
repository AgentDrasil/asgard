import type { RouteParamsGeneric } from "vue-router";

/**
 * Utility functions for frontend route parsing, building, and view resolution.
 */

/**
 * Parses and decodes file path parameters from route params.
 * Handles string or array parameter forms and filters empty segments.
 */
export function parseFilePath(param: string | string[] | null | undefined): string | null {
  if (param === undefined || param === null) {
    return null;
  }

  // vue-router 5.2.0 delivers route params already decoded per segment;
  // only filter empty segments and join with "/" (no re-decoding).
  const rawSegments = Array.isArray(param) ? param : param.split("/");
  const segments = rawSegments.filter(
    (seg) => seg !== undefined && seg !== null && seg.trim() !== "",
  );
  return segments.length > 0 ? segments.join("/") : null;
}

/**
 * Parses and normalizes commit IDs from route params case-insensitively.
 * Treats 'unstash', empty, or undefined as null (representing uncommitted workspace changes).
 */
export function parseCommitId(param: string | string[] | null | undefined): string | null {
  if (param === undefined || param === null) {
    return null;
  }

  // Defensively handle array form by picking first segment if delivered as string[]
  const raw = Array.isArray(param) ? param[0] : param;
  if (!raw) {
    return null;
  }

  const trimmed = raw.trim();
  if (!trimmed || trimmed.toLowerCase() === "unstash") {
    return null;
  }

  // Delivered value is already decoded by vue-router; return as-is.
  return trimmed;
}

/**
 * Encodes file path segments preserving directory slashes while encoding special characters.
 */
export function encodePathSegments(filePath: string): string {
  if (!filePath) {
    return "";
  }

  return filePath
    .split("/")
    .filter((seg) => seg.length > 0)
    .map((seg) => encodeURIComponent(seg))
    .join("/");
}

/**
 * Builds the URL path for the chat view.
 */
export function buildChatRoute(sessionId: string): string {
  return `/chat/${encodeURIComponent(sessionId)}`;
}

/**
 * Builds the URL path for the file browser view.
 */
export function buildFilesRoute(sessionId: string, filePath?: string | null): string {
  const base = `/chat/${encodeURIComponent(sessionId)}/files`;
  if (!filePath) {
    return base;
  }
  const encodedPath = encodePathSegments(filePath);
  return encodedPath ? `${base}/${encodedPath}` : base;
}

/**
 * Builds the URL path for the VCS / diff view.
 * Note: Callers must always generate VCS routes using this builder rather than manual concatenation,
 * because omitting the commitId segment manually (e.g. `/chat/:id/vcs/src/main.go`) would cause
 * the first path segment to be parsed as the commit ID.
 */
export function buildVcsRoute(
  sessionId: string,
  commitId?: string | null,
  filePath?: string | null,
): string {
  const normalizedCommit =
    commitId && commitId.trim().toLowerCase() !== "unstash" ? commitId.trim() : null;
  const commitSegment = normalizedCommit ? encodeURIComponent(normalizedCommit) : "unstash";
  const base = `/chat/${encodeURIComponent(sessionId)}/vcs/${commitSegment}`;
  if (!filePath) {
    return base;
  }
  const encodedPath = encodePathSegments(filePath);
  return encodedPath ? `${base}/${encodedPath}` : base;
}

/**
 * Checks whether the session state (such as drafts and temporary view state) should be reset
 * based on transitioning across different session IDs.
 */
export function shouldResetSessionState(
  prevSessionId: string | null | undefined,
  nextSessionId: string | null | undefined,
): boolean {
  const prev = prevSessionId ?? null;
  const next = nextSessionId ?? null;
  if (!prev || !next) {
    return false;
  }
  return prev !== next;
}

export interface ResolvedRouteView {
  activeView: "chat" | "file" | "vcs";
  filePath: string | null;
  commitId: string | null;
}

/**
 * Resolves activeView, filePath, and commitId from route location path, routeName, and params.
 */
export function resolveViewFromRoute(
  path: string,
  params: RouteParamsGeneric | Record<string, any>,
  routeName?: string | null,
): ResolvedRouteView {
  let activeView: "chat" | "file" | "vcs" = "chat";

  // Priority 1: Match by routeName if available
  if (routeName === "chat-vcs") {
    activeView = "vcs";
  } else if (routeName === "chat-files") {
    activeView = "file";
  } else if (routeName === "chat") {
    activeView = "chat";
  } else {
    // Priority 2: Segment-anchored regex matching (check /vcs before /files)
    if (/^\/chat\/[^/]+\/vcs(\/|$)/.test(path)) {
      activeView = "vcs";
    } else if (/^\/chat\/[^/]+\/files(\/|$)/.test(path)) {
      activeView = "file";
    } else {
      activeView = "chat";
    }
  }

  const filePath = parseFilePath(params.filePath as string | string[] | undefined);
  const commitId =
    activeView === "vcs" ? parseCommitId(params.commitId as string | string[] | undefined) : null;

  return {
    activeView,
    filePath,
    commitId,
  };
}
