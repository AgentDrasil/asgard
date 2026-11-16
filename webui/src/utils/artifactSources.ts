import type { SubagentArtifacts } from "../types";

// A chat lists the artifacts of its descendant subagents next to its own. An
// artifact of a subagent opens with the subagent's session id (its /tmp and
// /session are its own), so it is addressed by a key that carries the chat id:
// "subagent:<chatId>:<path>". The /shared directory belongs to the whole spawn
// tree and opens with any chat of it, so its artifacts keep the plain path as
// key (and are deduplicated across the chats that produced them).
const KEY_PREFIX = "subagent:";

export interface ArtifactSource {
  chatId: string;
  label: string;
  agent?: string;
}

export interface ArtifactTarget {
  /** The chat whose file API serves the path, or null for the viewed chat. */
  sessionId: string | null;
  path: string;
}

export function isSharedArtifactPath(path: string): boolean {
  const clean = path.replace(/\\/g, "/");
  return clean === "/shared" || clean.startsWith("/shared/");
}

export function subagentArtifactKey(chatId: string, path: string): string {
  return isSharedArtifactPath(path) ? path : `${KEY_PREFIX}${chatId}:${path}`;
}

export function parseArtifactKey(key: string | null | undefined): ArtifactTarget {
  if (!key) return { sessionId: null, path: "" };
  if (key.startsWith(KEY_PREFIX)) {
    const rest = key.slice(KEY_PREFIX.length);
    const i = rest.indexOf(":");
    if (i > 0) return { sessionId: rest.slice(0, i), path: rest.slice(i + 1) };
  }
  return { sessionId: null, path: key };
}

// sourceName is how a subagent is named to the user.
export function sourceName(s: { label?: string; agent?: string; chatId: string }): string {
  return s.label || s.agent || s.chatId.slice(0, 8);
}

export interface SubagentArtifactEntries {
  keys: string[];
  /** The producing subagent of every key. */
  sources: Record<string, ArtifactSource>;
}

// subagentArtifactEntries flattens the descendants' artifacts into keys, in the
// order they were produced, skipping keys in `skip` (the chat's own artifacts).
export function subagentArtifactEntries(
  items: SubagentArtifacts[] | undefined,
  skip: Iterable<string> = [],
): SubagentArtifactEntries {
  const seen = new Set<string>(skip);
  const keys: string[] = [];
  const sources: Record<string, ArtifactSource> = {};
  for (const item of items ?? []) {
    for (const path of item.artifacts ?? []) {
      if (!path) continue;
      const key = subagentArtifactKey(item.chatId, path);
      if (seen.has(key)) continue;
      seen.add(key);
      keys.push(key);
      sources[key] = { chatId: item.chatId, label: sourceName(item), agent: item.agent };
    }
  }
  return { keys, sources };
}

export interface SubagentArtifactPayload {
  id?: unknown;
  label?: unknown;
  agent?: unknown;
  artifacts?: unknown;
}

// mergeSubagentArtifacts applies a "subagent_artifact" event to the list.
export function mergeSubagentArtifacts(
  list: SubagentArtifacts[] | undefined,
  p: SubagentArtifactPayload | undefined,
): SubagentArtifacts[] {
  const current = list ?? [];
  const id = typeof p?.id === "string" ? p.id : "";
  const files = Array.isArray(p?.artifacts)
    ? (p.artifacts as unknown[]).filter((f): f is string => typeof f === "string" && f !== "")
    : [];
  if (!id || files.length === 0) return current;
  const label = typeof p?.label === "string" ? p.label : "";
  const agent = typeof p?.agent === "string" ? p.agent : undefined;
  const idx = current.findIndex((s) => s.chatId === id);
  if (idx < 0) {
    return [...current, { chatId: id, label, agent, artifacts: Array.from(new Set(files)) }];
  }
  const prev = current[idx];
  const next: SubagentArtifacts = {
    ...prev,
    label: label || prev.label,
    agent: agent ?? prev.agent,
    artifacts: Array.from(new Set([...prev.artifacts, ...files])),
  };
  return [...current.slice(0, idx), next, ...current.slice(idx + 1)];
}
