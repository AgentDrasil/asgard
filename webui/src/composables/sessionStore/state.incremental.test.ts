import { describe, it, expect } from "vitest";
import { ref, watch, nextTick } from "vue";
import { createSessionState } from "./state";
import { isToolMessage, TOOL_ITEM_DELIMITER } from "../../utils/messageUtils";
import type { AgentInfo, ChatMessage } from "../../types";

// Verbatim copy of the original non-incremental merge, used as the oracle.
function referenceMerge(msgs: ChatMessage[]): ChatMessage[] {
  const out: ChatMessage[] = [];
  let i = 0;
  const mergeFiles = (target: ChatMessage, source: ChatMessage) => {
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
  };
  while (i < msgs.length) {
    const cur = msgs[i];
    if (!isToolMessage(cur)) {
      out.push(cur);
      i += 1;
      continue;
    }
    const agentName = cur.agentName;
    const items: string[] = [];
    const mergedMsg: ChatMessage = { ...cur, role: "activity", activityType: "TOOL" };
    while (i < msgs.length && isToolMessage(msgs[i]) && msgs[i].agentName === agentName) {
      const toolMsg = msgs[i];
      const nextMsg = msgs[i + 1];
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

// Small deterministic PRNG so failures reproduce.
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

describe("incremental messages view", () => {
  for (let seed = 1; seed <= 40; seed++) {
    it(`matches the full merge after random mutations (seed ${seed})`, () => {
      const rand = rng(seed);
      const pick = <T>(xs: T[]) => xs[Math.floor(rand() * xs.length)];
      const state = createSessionState(ref<AgentInfo[]>([]));
      let nextId = 0;
      const makeMsg = (id?: string): ChatMessage => {
        const role = pick([
          "tool_call",
          "tool_result",
          "tool_call",
          "tool_result",
          "assistant",
          "user",
          "activity",
        ]);
        return {
          id: id ?? `m${nextId++}`,
          role: role as ChatMessage["role"],
          content: `c${Math.floor(rand() * 4)}`,
          timestamp: nextId,
          agentName: pick(["a", "b"]),
          stepIndex: pick([undefined, 1, 2]),
          activityType: role === "activity" ? pick([undefined, "TOOL", "TEXT"] as any) : undefined,
          targetFiles: rand() < 0.2 ? [`f${Math.floor(rand() * 3)}`] : undefined,
          artifactFiles: rand() < 0.2 ? [`g${Math.floor(rand() * 3)}`] : undefined,
        } as ChatMessage;
      };
      const ids = () => state.rawMessages.value.map((m) => m.id);

      for (let step = 0; step < 300; step++) {
        const raw = state.rawMessages.value;
        const op = rand();
        if (op < 0.45 || raw.length === 0) {
          state.upsertMessage(makeMsg());
        } else if (op < 0.65) {
          // streamed update of an existing message (any position)
          const id = pick(ids());
          state.upsertMessage({ ...makeMsg(id) });
        } else if (op < 0.75) {
          const id = pick(ids());
          const existing = raw.find((m) => m.id === id)!;
          state.replaceMessage(id, { ...existing, replied: true, replyText: "r" });
        } else if (op < 0.82) {
          // filter (deletion)
          const id = pick(ids());
          state.rawMessages.value = raw.filter((m) => m.id !== id);
        } else if (op < 0.9) {
          // snapshot merge: reorder/replace/append like refreshActiveSnapshot
          const snap = raw
            .filter(() => rand() < 0.9)
            .map((m) => (rand() < 0.3 ? { ...m, content: "snap" } : m));
          snap.push(makeMsg());
          state.rawMessages.value = snap;
        } else if (op < 0.93) {
          state.rawMessages.value = [];
        } else {
          state.rawMessages.value = raw.map((m) => (rand() < 0.1 ? { ...m, content: "map" } : m));
        }
        // Read at random times so several mutations can batch between reads.
        const checkNow = rand() < 0.7;
        const got = checkNow ? state.messages.value : [];
        const want = checkNow ? referenceMerge(state.rawMessages.value) : [];
        expect(got).toEqual(want);
      }
      expect(state.messages.value).toEqual(referenceMerge(state.rawMessages.value));
    });
  }

  it("fires a non-deep watcher on `messages` for a streamed update and an append", async () => {
    const state = createSessionState(ref<AgentInfo[]>([]));
    const a: ChatMessage = { id: "a", role: "assistant", content: "x", timestamp: 1 };
    state.upsertMessage(a);
    await nextTick();
    let fired = 0;
    watch(state.messages, () => fired++);

    state.upsertMessage({ ...a, content: "xy" });
    await nextTick();
    expect(fired).toBe(1);
    expect(state.messages.value[0].content).toBe("xy");

    state.upsertMessage({ id: "b", role: "user", content: "q", timestamp: 2 });
    await nextTick();
    expect(fired).toBe(2);
  });
});
