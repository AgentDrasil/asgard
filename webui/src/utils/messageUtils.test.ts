import { describe, it, expect } from "vitest";
import {
  mergeToolMessages,
  TOOL_ITEM_DELIMITER,
  isToolMessage,
  parseInterAgentMessage,
} from "./messageUtils";
import type { ChatMessage } from "../types";

describe("messageUtils", () => {
  describe("isToolMessage", () => {
    it("identifies tool messages", () => {
      expect(isToolMessage({ id: "1", role: "tool_call", content: "", timestamp: 0 })).toBe(true);
      expect(isToolMessage({ id: "2", role: "tool_result", content: "", timestamp: 0 })).toBe(true);
      expect(
        isToolMessage({
          id: "3",
          role: "activity",
          activityType: "TOOL",
          content: "",
          timestamp: 0,
        }),
      ).toBe(true);
      expect(isToolMessage({ id: "4", role: "user", content: "", timestamp: 0 })).toBe(false);
      expect(isToolMessage({ id: "5", role: "assistant", content: "", timestamp: 0 })).toBe(false);
    });
  });

  describe("mergeToolMessages", () => {
    it("groups consecutive tool_result messages into a single activity message", () => {
      const msgs: ChatMessage[] = [
        {
          id: "step-1",
          role: "tool_result",
          agentName: "Coder",
          content: "> ls\n\nfile1.txt",
          timestamp: 100,
        },
        {
          id: "step-2",
          role: "tool_result",
          agentName: "Coder",
          content: "> cat file1.txt\n\nhello",
          timestamp: 200,
        },
      ];

      const merged = mergeToolMessages(msgs);
      expect(merged.length).toBe(1);
      expect(merged[0].role).toBe("activity");
      expect(merged[0].activityType).toBe("TOOL");
      expect(merged[0].agentName).toBe("Coder");
      expect(merged[0].content).toBe(
        `> ls\n\nfile1.txt\n${TOOL_ITEM_DELIMITER}\n> cat file1.txt\n\nhello`,
      );
    });

    it("pairs tool_call and tool_result without duplicating content", () => {
      const msgs: ChatMessage[] = [
        {
          id: "step-1-call",
          role: "tool_call",
          agentName: "Coder",
          stepIndex: 1,
          content: "> ls",
          timestamp: 100,
        },
        {
          id: "step-1-result",
          role: "tool_result",
          agentName: "Coder",
          stepIndex: 1,
          content: "> ls\n\nfile1.txt",
          timestamp: 150,
        },
      ];

      const merged = mergeToolMessages(msgs);
      expect(merged.length).toBe(1);
      expect(merged[0].content).toBe("> ls\n\nfile1.txt");
    });

    it("does not merge tools from different agents", () => {
      const msgs: ChatMessage[] = [
        {
          id: "step-1",
          role: "tool_result",
          agentName: "Plan Reviewer",
          content: "> ls\n\nplan.md",
          timestamp: 100,
        },
        {
          id: "step-2",
          role: "tool_result",
          agentName: "Coder",
          content: "> cat plan.md\n\nplan details",
          timestamp: 200,
        },
      ];

      const merged = mergeToolMessages(msgs);
      expect(merged.length).toBe(2);
      expect(merged[0].agentName).toBe("Plan Reviewer");
      expect(merged[1].agentName).toBe("Coder");
    });

    it("preserves non-tool messages in exact order", () => {
      const msgs: ChatMessage[] = [
        { id: "u1", role: "user", content: "Do it", timestamp: 50 },
        { id: "t1", role: "tool_result", content: "> run", timestamp: 100 },
        { id: "a1", role: "assistant", content: "Done", timestamp: 200 },
      ];

      const merged = mergeToolMessages(msgs);
      expect(merged.length).toBe(3);
      expect(merged[0].role).toBe("user");
      expect(merged[1].role).toBe("activity");
      expect(merged[2].role).toBe("assistant");
    });
  });
});

describe("parseInterAgentMessage", () => {
  it("strips the parent prefix", () => {
    expect(parseInterAgentMessage("[message from parent agent lead]\nplease stop")).toEqual({
      kind: "parent",
      from: "lead",
      body: "please stop",
    });
  });

  it("strips the subagent prefix and keeps the label", () => {
    expect(parseInterAgentMessage("[message from subagent c-1 (review)]\nfound 2 issues")).toEqual({
      kind: "subagent",
      from: "c-1 (review)",
      body: "found 2 issues",
    });
  });

  it("parses a finish notice with its state", () => {
    expect(
      parseInterAgentMessage("[subagent c-1 (review) finished: failed] boom; full output: x"),
    ).toEqual({
      kind: "finished",
      from: "c-1 (review)",
      state: "failed",
      body: "boom; full output: x",
    });
  });

  it("parses a question with its id, sender and the answer hint in the body", () => {
    const text =
      "[question q-1 from Reviewer c-2 (rev)]\nwhich branch?\nAnswer with: subagent answer q-1 -- <text>";
    expect(parseInterAgentMessage(text)).toEqual({
      kind: "question",
      qid: "q-1",
      from: "Reviewer c-2 (rev)",
      body: "which branch?\nAnswer with: subagent answer q-1 -- <text>",
    });
  });

  it("parses a question and an answer without a label", () => {
    expect(parseInterAgentMessage("[question q-1 from Lead c-0]\nok?")).toEqual({
      kind: "question",
      qid: "q-1",
      from: "Lead c-0",
      body: "ok?",
    });
    expect(parseInterAgentMessage("[answer q-1 from Reviewer c-2 (rev)]\nmain")).toEqual({
      kind: "answer",
      qid: "q-1",
      from: "Reviewer c-2 (rev)",
      body: "main",
    });
  });

  it("does not take a question or answer that is not at the start", () => {
    expect(parseInterAgentMessage("see [question q-1 from A c]\nx")).toBeNull();
    expect(parseInterAgentMessage("[question q-1]\nx")).toBeNull();
  });

  it("leaves ordinary text alone", () => {
    expect(parseInterAgentMessage("hello [message from parent agent x]")).toBeNull();
    expect(parseInterAgentMessage("")).toBeNull();
  });
});
