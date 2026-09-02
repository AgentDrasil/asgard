import type { ChatMessage } from "../types";
import { parseOptions } from "./askUserOptions";
import { getMessageArtifactFiles } from "./messageUtils";
import { t } from "../i18n";

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
      statusText: t("chat.workflow.waitingForInputStatus", { agent: agentLabel }),
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
      statusText: t("chat.workflow.runningStatus", { agent: agentLabel }),
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
    if (lastBusinessMsg.role === "assistant") {
      return {
        stage: "completed",
        pendingMessage: null,
        pendingMessages: [],
        options: [],
        statusText: t("chat.workflow.completed"),
        targetFiles: [],
        artifactFiles: [],
      };
    }
  }

  // 4. idle: No business messages and not running
  return {
    stage: "idle",
    pendingMessage: null,
    pendingMessages: [],
    options: [],
    statusText: t("chat.workflow.readyStatus", { agent: activeAgentName || "Workflow" }),
    targetFiles: [],
    artifactFiles: [],
  };
}
