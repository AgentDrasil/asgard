import { describe, it, expect } from "vitest";
import { computeWorkflowPanelState } from "./workflowPanelState";
import type { ChatMessage } from "../types";

describe("workflowPanelState", () => {
  describe("computeWorkflowPanelState", () => {
    it("determines stage as idle when messages list is empty and running is false", () => {
      const state = computeWorkflowPanelState({
        running: false,
        messages: [],
        activeAgentName: "Architecture Review",
      });
      expect(state.stage).toBe("idle");
      expect(state.statusText).toContain("Architecture Review");
    });

    it("determines stage as running when running is true and no unreplied ask_user", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Start review" },
        { id: "2", role: "activity", content: "Analyzing codebase" },
      ];
      const state = computeWorkflowPanelState({
        running: true,
        messages,
        workingAgentLabel: "Architect",
      });
      expect(state.stage).toBe("running");
      expect(state.statusText).toBe("Architect is running...");
    });

    it("prioritizes waiting_human over running when an unreplied ask_user message exists", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Start review" },
        {
          id: "2",
          role: "ask_user",
          agentName: "Architect",
          content: "Do you agree with this design?\nOptions: Approve / Request Changes",
          artifactFiles: ["plan.md"],
          replied: false,
        },
      ];
      const state = computeWorkflowPanelState({
        running: true,
        messages,
        workingAgentLabel: "Architect",
      });
      expect(state.stage).toBe("waiting_human");
      expect(state.pendingMessage?.id).toBe("2");
      expect(state.pendingMessages.length).toBe(1);
      expect(state.options).toEqual(["Approve", "Request Changes"]);
      expect(state.artifactFiles).toEqual(["plan.md"]);
      expect(state.statusText).toBe("Architect is waiting for human input");
    });

    it("collects all unreplied ask_user messages in pendingMessages when multiple exist", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Start review" },
        {
          id: "2",
          role: "ask_user",
          agentName: "Intent Analyst",
          content: "Question 1?\nOptions: A / B",
          replied: false,
        },
        {
          id: "3",
          role: "ask_user",
          agentName: "Architect",
          content: "Question 2?\nOptions: Yes / No",
          replied: false,
        },
      ];
      const state = computeWorkflowPanelState({
        running: false,
        messages,
      });
      expect(state.stage).toBe("waiting_human");
      expect(state.pendingMessages.length).toBe(2);
      expect(state.pendingMessages[0].id).toBe("2");
      expect(state.pendingMessages[1].id).toBe("3");
    });

    it("ignores replied ask_user messages for waiting_human stage", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Start review" },
        {
          id: "2",
          role: "ask_user",
          content: "Options: Proceed / Stop",
          replied: true,
          replyText: "Proceed",
        },
        { id: "3", role: "assistant", content: "Finished workflow successfully." },
      ];
      const state = computeWorkflowPanelState({
        running: false,
        messages,
      });
      expect(state.stage).toBe("completed");
    });

    it("determines stage as failed when last business message is error", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Run task" },
        { id: "2", role: "error", content: "Compilation failed with exit code 1" },
      ];
      const state = computeWorkflowPanelState({
        running: false,
        messages,
      });
      expect(state.stage).toBe("failed");
      expect(state.statusText).toBe("Compilation failed with exit code 1");
    });

    it("determines stage as failed even when error message is followed by intermediate activity or tool messages", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Run task" },
        { id: "2", role: "error", content: "Fatal execution error" },
        { id: "3", role: "activity", content: "Cleaning up sandbox" },
        { id: "4", role: "tool_result", content: "Sandbox cleanup done" },
        { id: "5", role: "reasoning", content: "Done" },
      ];
      const state = computeWorkflowPanelState({
        running: false,
        messages,
      });
      expect(state.stage).toBe("failed");
      expect(state.statusText).toBe("Fatal execution error");
    });

    it("determines stage as completed when execution finishes without error", () => {
      const messages: ChatMessage[] = [
        { id: "1", role: "user", content: "Run task" },
        { id: "2", role: "assistant", content: "Workflow executed successfully." },
        { id: "3", role: "activity", content: "Session finished" },
      ];
      const state = computeWorkflowPanelState({
        running: false,
        messages,
      });
      expect(state.stage).toBe("completed");
      expect(state.statusText).toBe("Workflow completed");
    });
  });
});
