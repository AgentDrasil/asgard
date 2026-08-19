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
export function isToolMessage(msg: ChatMessage | undefined): boolean {
  if (!msg) return false;
  return (
    msg.role === "tool_call" ||
    msg.role === "tool_result" ||
    (msg.role === "activity" &&
      (msg.activityType === "TOOL" ||
        msg.activityType === "TOOL_CALL" ||
        msg.activityType === "TOOL_RESULT" ||
        msg.activityType === undefined))
  );
}

export function mergeToolMessages(msgs: ChatMessage[]): ChatMessage[] {
  const out: ChatMessage[] = [];
  let i = 0;

  while (i < msgs.length) {
    const cur = msgs[i];
    if (!isToolMessage(cur)) {
      out.push(cur);
      i += 1;
      continue;
    }

    // Start a merged tool group for consecutive tool messages from the same agent
    const agentName = cur.agentName;
    const items: string[] = [];
    const mergedMsg: ChatMessage = {
      ...cur,
      role: "activity",
      activityType: "TOOL",
    };

    while (i < msgs.length && isToolMessage(msgs[i]) && msgs[i].agentName === agentName) {
      const toolMsg = msgs[i];
      const nextMsg = msgs[i + 1];

      // Check if current is a tool_call and next is its matching tool_result
      if (
        toolMsg.role === "tool_call" &&
        nextMsg?.role === "tool_result" &&
        (toolMsg.stepIndex == null ||
          nextMsg.stepIndex == null ||
          toolMsg.stepIndex === nextMsg.stepIndex)
      ) {
        const contentToAdd = nextMsg.content.includes(toolMsg.content)
          ? nextMsg.content
          : `${toolMsg.content}\n\n${nextMsg.content}`;
        items.push(contentToAdd);
        mergeFiles(mergedMsg, toolMsg);
        mergeFiles(mergedMsg, nextMsg);
        i += 2;
      } else {
        items.push(toolMsg.content);
        mergeFiles(mergedMsg, toolMsg);
        i += 1;
      }
    }

    mergedMsg.content = items.join(`\n${TOOL_ITEM_DELIMITER}\n`);
    out.push(mergedMsg);
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
