import type { ChatSession, SubagentState } from "../types";

export interface SessionRow {
  session: ChatSession;
  depth: number;
  childCount: number;
  collapsed: boolean;
}

// subagentLabel is the name a subagent chat goes by in lists.
export function subagentLabel(s: ChatSession, fallback: string): string {
  return s.spawnLabel || s.title || fallback;
}

/** Returns the DaisyUI badge class for a subagent lifecycle state. */
export function subagentStateBadgeClass(state?: SubagentState): string {
  switch (state) {
    case "running":
      return "badge-info";
    case "queued":
      return "badge-warning";
    case "failed":
      return "badge-error";
    case "killed":
      return "badge-neutral";
    default:
      return "badge-success";
  }
}

// childrenByParent groups the sessions that have a parent present in the list.
// A child whose parent is not listed (archived, beyond the page limit) is a
// root, so it never disappears.
function indexSessions(sessions: ChatSession[]) {
  const ids = new Set(sessions.map((s) => s.chatID));
  const children = new Map<string, ChatSession[]>();
  const roots: ChatSession[] = [];
  for (const s of sessions) {
    if (s.parentChatId && s.parentChatId !== s.chatID && ids.has(s.parentChatId)) {
      const list = children.get(s.parentChatId) ?? [];
      list.push(s);
      children.set(s.parentChatId, list);
    } else {
      roots.push(s);
    }
  }
  return { children, roots };
}

// rootOf walks up to the root session of s within the list.
export function rootOf(s: ChatSession, byId: Map<string, ChatSession>): ChatSession {
  let cur = s;
  const seen = new Set<string>();
  while (cur.parentChatId && byId.has(cur.parentChatId) && !seen.has(cur.chatID)) {
    seen.add(cur.chatID);
    cur = byId.get(cur.parentChatId)!;
  }
  return cur;
}

/** Returns rootId together with all of its descendant subagent chat IDs. */
export function descendantIds(sessions: ChatSession[], rootId: string): Set<string> {
  const { children } = indexSessions(sessions);
  const ids = new Set<string>();
  const stack = [rootId];
  while (stack.length > 0) {
    const id = stack.pop()!;
    if (ids.has(id)) continue;
    ids.add(id);
    for (const kid of children.get(id) ?? []) stack.push(kid.chatID);
  }
  return ids;
}

// flattenSessions lays the sessions out as tree rows: every root followed by
// its subagents, depth first. Children of a collapsed session are left out.
export function flattenSessions(
  sessions: ChatSession[],
  collapsed: Record<string, boolean> = {},
): SessionRow[] {
  const { children, roots } = indexSessions(sessions);
  const rows: SessionRow[] = [];
  const visited = new Set<string>();
  const walk = (s: ChatSession, depth: number) => {
    if (visited.has(s.chatID)) return;
    visited.add(s.chatID);
    const kids = children.get(s.chatID) ?? [];
    const isCollapsed = !!collapsed[s.chatID];
    rows.push({ session: s, depth, childCount: kids.length, collapsed: isCollapsed });
    if (!isCollapsed) for (const k of kids) walk(k, depth + 1);
  };
  for (const r of roots) walk(r, 0);
  return rows;
}
