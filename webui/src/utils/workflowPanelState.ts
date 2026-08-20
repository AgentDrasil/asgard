import type { ChatMessage } from "../types";
import { parseOptions } from "./askUserOptions";
import { getMessageArtifactFiles } from "./messageUtils";

export type WorkflowStage = "running" | "waiting_human" | "completed" | "failed" | "idle";

export interface WorkflowPanelState {
  stage: WorkflowStage;
  pendingMessage?: ChatMessage | null;
  pendingMessages: ChatMessage[];
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

  // 1. waiting_human (highest priority): Look for all unreplied ask_user messages
  const pendingMessages = messages.filter((m) => m.role === "ask_user" && !m.replied);
  if (pendingMessages.length > 0) {
    const latest = pendingMessages[pendingMessages.length - 1];
    const options = parseOptions(latest.content);
    const targetFiles = latest.targetFiles || [];
    const artifactFiles = getMessageArtifactFiles(latest);
    const agentLabel = latest.agentName || activeAgentName || "Workflow";
    return {
      stage: "waiting_human",
      pendingMessage: latest,
      pendingMessages,
      options,
      statusText: `${agentLabel} is waiting for human input`,
      targetFiles,
      artifactFiles,
    };
  }

  // 2. running: if running is true and no unreplied ask_user exists
  if (running) {
    const agentLabel = workingAgentLabel || activeAgentName || "Workflow";
    return {
      stage: "running",
      pendingMessage: null,
      pendingMessages: [],
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
        pendingMessages: [],
        options: [],
        statusText: lastBusinessMsg.content || "Workflow execution failed",
        targetFiles: lastBusinessMsg.targetFiles || [],
        artifactFiles: getMessageArtifactFiles(lastBusinessMsg),
      };
    }
    return {
      stage: "completed",
      pendingMessage: null,
      pendingMessages: [],
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
    pendingMessages: [],
    options: [],
    statusText: `${activeAgentName || "Workflow"} is ready`,
    targetFiles: [],
    artifactFiles: [],
  };
}
