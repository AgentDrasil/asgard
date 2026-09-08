// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import WorkflowControlPanel from "./WorkflowControlPanel.vue";
import type { AgentInfo, ChatMessage, WorkflowRunSummary } from "../../types";
import { i18n } from "../../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

const { sendAskUserReply, getSessionWorkflows, redriveWorkflowRun } = vi.hoisted(() => ({
  sendAskUserReply:
    vi.fn<(sessionId: string, messageId: string, text: string) => Promise<boolean>>(),
  getSessionWorkflows: vi.fn<(sessionId: string) => Promise<WorkflowRunSummary[]>>(),
  redriveWorkflowRun: vi.fn<(runId: string) => Promise<boolean>>(),
}));

vi.mock("../../lib/api", () => ({
  sendAskUserReply,
  getSessionWorkflows,
  redriveWorkflowRun,
}));

const flushPromises = () => new Promise((resolve) => setTimeout(resolve, 0));

const workflowAgent: AgentInfo = {
  id: "workflow-agent",
  type: "workflow",
  name: "Code Review Workflow",
  description: "",
  run_dirs: [],
};

const failedMessages: ChatMessage[] = [
  {
    id: "user-1",
    role: "user",
    content: "review the change",
    timestamp: 1725120000000,
  },
  {
    id: "wf-error-plan",
    role: "error",
    agentName: "Code Review Workflow",
    content: "workflow failed: node plan failed",
    timestamp: 1725120001000,
  },
];

describe("WorkflowControlPanel.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.clearAllMocks();
    sendAskUserReply.mockResolvedValue(true);
    getSessionWorkflows.mockResolvedValue([]);
    redriveWorkflowRun.mockResolvedValue(true);
  });

  const mountPanel = async (messages: ChatMessage[]) => {
    const app = createApp({
      render() {
        return h(WorkflowControlPanel, {
          activeAgent: workflowAgent,
          loading: false,
          messages,
          sessionId: "chat-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await flushPromises();
    await nextTick();
    return app;
  };

  it("shows a redrive button when the panel stage is failed and a FAILED run exists", async () => {
    getSessionWorkflows.mockResolvedValue([
      { runId: "run-failed-1", status: "FAILED", updatedAt: "2026-09-01T00:00:00Z" },
      { runId: "run-ok-1", status: "COMPLETED" },
    ]);

    const app = await mountPanel(failedMessages);

    const btn = root.querySelector<HTMLButtonElement>('[data-testid="workflow-redrive-button"]');
    expect(btn).not.toBeNull();
    expect(btn?.textContent).toContain("Re-run");
    expect(getSessionWorkflows).toHaveBeenCalledWith("chat-123");

    btn?.click();
    await flushPromises();
    await nextTick();
    expect(redriveWorkflowRun).toHaveBeenCalledWith("run-failed-1");

    app.unmount();
  });

  it("hides the redrive button when no FAILED run backs the failed stage", async () => {
    getSessionWorkflows.mockResolvedValue([
      { runId: "run-ok-1", status: "COMPLETED" },
      { runId: "run-cancel-1", status: "CANCELLED" },
    ]);

    const app = await mountPanel(failedMessages);

    expect(root.querySelector('[data-testid="workflow-redrive-button"]')).toBeNull();

    app.unmount();
  });

  it("shows an error message and re-enables the button when the redrive request is rejected", async () => {
    getSessionWorkflows.mockResolvedValue([{ runId: "run-failed-1", status: "FAILED" }]);
    redriveWorkflowRun.mockResolvedValue(false);

    const app = await mountPanel(failedMessages);

    const btn = root.querySelector<HTMLButtonElement>('[data-testid="workflow-redrive-button"]');
    expect(btn).not.toBeNull();
    btn?.click();
    await flushPromises();
    await nextTick();

    expect(root.textContent).toContain("Re-run request failed, please retry");
    const again = root.querySelector<HTMLButtonElement>('[data-testid="workflow-redrive-button"]');
    expect(again).not.toBeNull();
    expect(again?.disabled).toBe(false);

    app.unmount();
  });

  it("does not surface redrive when the session has no failed stage", async () => {
    const app = await mountPanel([
      {
        id: "user-1",
        role: "user",
        content: "review the change",
        timestamp: 1725120000000,
      },
      {
        id: "wf-summary-1",
        role: "assistant",
        content: "workflow completed",
        timestamp: 1725120001000,
      },
    ]);

    expect(root.querySelector('[data-testid="workflow-redrive-button"]')).toBeNull();
    expect(getSessionWorkflows).not.toHaveBeenCalledWith("chat-123");

    app.unmount();
  });
});
