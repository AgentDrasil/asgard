import type { ChatMessage } from "../types";
import { parseOptions } from "./askUserOptions";
import { getMessageArtifactFiles } from "./messageUtils";

export type WorkflowStage = "running" | "waiting_human" | "completed" | "failed" | "idle";

export interface WorkflowPanelState {
  stage: WorkflowStage;
  pendingMessage?: ChatMessage | null;
  options: string[];
  statusText: string;
  targetFiles: string[];
  artifactFiles: string[];
}

export interface ComputeWorkflowPanelStateParams {
  running: boolean;
  messages: ChatMessage[];
  workingAgentLabel?: string | null;
  activeAgentName?: string | null;
}

const IGNORED_ROLES = new Set([
  "activity",
  "tool_call",
  "tool_result",
  "reasoning",
  "system",
  "developer",
]);

export function computeWorkflowPanelState(
  params: ComputeWorkflowPanelStateParams,
): WorkflowPanelState {
  const { running, messages, workingAgentLabel, activeAgentName } = params;

  // 1. waiting_human (highest priority): Look for latest unreplied ask_user message from the end
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (msg.role === "ask_user" && !msg.replied) {
      const options = parseOptions(msg.content);
      const targetFiles = msg.targetFiles || [];
      const artifactFiles = getMessageArtifactFiles(msg);
      const agentLabel = msg.agentName || activeAgentName || "Workflow";
      return {
        stage: "waiting_human",
        pendingMessage: msg,
        options,
        statusText: `${agentLabel} is waiting for human input`,
        targetFiles,
        artifactFiles,
      };
    }
  }

  // 2. running: if running is true and no unreplied ask_user exists
  if (running) {
    const agentLabel = workingAgentLabel || activeAgentName || "Workflow";
    return {
      stage: "running",
      pendingMessage: null,
      options: [],
      statusText: `${agentLabel} is running...`,
      targetFiles: [],
      artifactFiles: [],
    };
  }

  // 3. Scan backwards skipping intermediate process messages
  let lastBusinessMsg: ChatMessage | null = null;
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (!IGNORED_ROLES.has(msg.role)) {
      lastBusinessMsg = msg;
      break;
    }
  }

  if (lastBusinessMsg) {
    if (lastBusinessMsg.role === "error") {
      return {
        stage: "failed",
        pendingMessage: null,
        options: [],
        statusText: lastBusinessMsg.content || "Workflow execution failed",
        targetFiles: lastBusinessMsg.targetFiles || [],
        artifactFiles: getMessageArtifactFiles(lastBusinessMsg),
      };
    }
    return {
      stage: "completed",
      pendingMessage: null,
      options: [],
      statusText: "Workflow completed",
      targetFiles: [],
      artifactFiles: [],
    };
  }

  // 4. idle: No business messages and not running
  return {
    stage: "idle",
    pendingMessage: null,
    options: [],
    statusText: `${activeAgentName || "Workflow"} is ready`,
    targetFiles: [],
    artifactFiles: [],
  };
}
