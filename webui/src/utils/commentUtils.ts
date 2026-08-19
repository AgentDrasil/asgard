import type { CommentEntry } from "../types";

/**
 * Returns a unique key for identifying a comment.
 * If side is provided (e.g. for split diffs), it is included in the key.
 */
export function commentKey(filePath: string, lineNumber: number, side?: string): string {
  if (side) {
    return `${filePath}:${side}:${lineNumber}`;
  }
  return `${filePath}:${lineNumber}`;
}

/**
 * Formats a single CommentEntry into a standard chat input block format.
 */
export function formatCommentBlock(entry: CommentEntry): string {
  return `${entry.filePath} line ${entry.lineNumber}\n${entry.lineContent}\n---\n\nuser comment:\n\n${entry.comment}\n---`;
}

/**
 * Serializes all comment entries into a unified string suitable for chat input.
 * Returns an empty string if there are no comments.
 */
export function rebuildChatInputFromComments(
  comments: Map<string, CommentEntry> | CommentEntry[],
): string {
  const entries = comments instanceof Map ? Array.from(comments.values()) : comments;
  if (!entries || entries.length === 0) {
    return "";
  }
  return entries.map(formatCommentBlock).join("\n\n");
}
