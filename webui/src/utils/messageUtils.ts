import type { ChatMessage } from "../types";

export const TOOL_ITEM_DELIMITER = "---TOOL_ITEM_DELIMITER---";

// mergeToolMessages collapses consecutive tool_call → tool_result pairs in
// a flat DB message list into a single activity bubble per tool invocation.
// Two messages are treated as a pair when:
//  - they are adjacent in the list
//  - the first has role "tool_call" and the second has role "tool_result"
//  - they share the same step_index (when present)
//
// The merged message keeps the tool_call role/activityType and concatenates
// the content as "call\nresult", separating distinct pairs with a blank line.
export function mergeToolMessages(msgs: ChatMessage[]): ChatMessage[] {
  const out: ChatMessage[] = [];
  let i = 0;
  while (i < msgs.length) {
    const cur = msgs[i];
    const next = msgs[i + 1];
    const curIsCall = cur.role === "tool_call";
    const nextIsResult =
      next?.role === "tool_result" &&
      (cur.stepIndex == null || next.stepIndex == null || cur.stepIndex === next.stepIndex);

    if (curIsCall && nextIsResult) {
      // Check if the previous out entry is also a merged tool bubble we can append to
      const prev = out[out.length - 1];
      if (
        prev?.role === "tool_call" &&
        (prev?.activityType === "TOOL_CALL" || prev?.activityType === "TOOL")
      ) {
        prev.activityType = "TOOL";
        // Append this pair to the running tool log with a special delimiter
        prev.content =
          prev.content +
          `\n${TOOL_ITEM_DELIMITER}\n` +
          cur.content +
          `\n${TOOL_ITEM_DELIMITER}\n` +
          next.content;
        mergeFiles(prev, cur);
        mergeFiles(prev, next);
      } else {
        const newMsg: ChatMessage = {
          ...cur,
          activityType: "TOOL",
          content: cur.content + `\n${TOOL_ITEM_DELIMITER}\n` + next.content,
        };
        mergeFiles(newMsg, next);
        out.push(newMsg);
      }
      i += 2; // consumed both
    } else if (curIsCall) {
      // Handle isolated tool_call (e.g. status updates)
      const prev = out[out.length - 1];
      if (
        prev?.role === "tool_call" &&
        (prev?.activityType === "TOOL_CALL" || prev?.activityType === "TOOL")
      ) {
        prev.activityType = "TOOL";
        prev.content = prev.content + `\n${TOOL_ITEM_DELIMITER}\n` + cur.content;
        mergeFiles(prev, cur);
      } else {
        out.push({
          ...cur,
          activityType: "TOOL",
        });
      }
      i += 1;
    } else {
      out.push(cur);
      i += 1;
    }
  }
  return out;
}

function mergeFiles(target: ChatMessage, source: ChatMessage) {
  if (source.targetFiles && source.targetFiles.length > 0) {
    target.targetFiles = Array.from(
      new Set([...(target.targetFiles || []), ...source.targetFiles]),
    );
  }
  if (source.artifactFiles && source.artifactFiles.length > 0) {
    target.artifactFiles = Array.from(
      new Set([...(target.artifactFiles || []), ...source.artifactFiles]),
    );
  }
}

export function getMessageArtifactFiles(msg: ChatMessage): string[] {
  return msg.artifactFiles || [];
}
