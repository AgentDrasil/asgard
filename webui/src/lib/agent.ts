import {
  ClientFactory,
  RestTransportFactory,
  JsonRpcTransportFactory,
  Client,
} from "@a2a-js/sdk/client";
import { Role, TaskState } from "@a2a-js/sdk";
import { apiFetch } from "./api";

// Keep a cache of client instances to avoid fetching agent-card every request
const clientCache: Record<string, Client> = {};

export async function getAgentClient(agentId: string, customBaseUrl?: string): Promise<Client> {
  if (clientCache[agentId]) {
    return clientCache[agentId];
  }

  const baseUrl = customBaseUrl || window.location.origin;
  // The agent endpoint — createFromUrl will append /.well-known/agent-card.json
  const endpoint = `${baseUrl}/agents/${agentId}/`;

  const factory = new ClientFactory({
    transports: [new RestTransportFactory({ fetchImpl: apiFetch }), new JsonRpcTransportFactory()],
    preferredTransports: ["HTTP+JSON"],
  });

  // v1.0: createFromUrl fetches + normalizes the AgentCard natively.
  // The Go server returns supportedInterfaces which the v1.0 SDK handles directly.
  const client = await factory.createFromUrl(endpoint);

  clientCache[agentId] = client;
  return client;
}

export interface StreamCallbacks {
  onText: (
    text: string,
    inputTokens?: number,
    maxTokens?: number,
    metadata?: Record<string, any>,
  ) => void;
  onReasoning?: (text: string) => void;
  onStatus?: (
    statusText: string,
    entryType?: string,
    state?: string,
    metadata?: Record<string, any>,
  ) => void;
  onError?: (err: Error) => void;
  onComplete?: () => void;
}

/** Extract plain text from a v1.0 Part array. */
function extractTextFromParts(parts: any[]): string {
  let text = "";
  for (const part of parts) {
    if (part?.content?.$case === "text") {
      text += part.content.value;
    }
  }
  return text;
}

function extractTokens(val: any): { inputTokens?: number; maxTokens?: number } {
  if (!val) return {};
  const meta = val.metadata || val.status?.message?.metadata || val.message?.metadata || {};
  const inputTokens = meta["input_tokens"] ?? meta["total_input_tokens"];
  const maxTokens = meta["max_tokens"] ?? meta["context_window_size"];
  return {
    inputTokens: typeof inputTokens === "number" ? inputTokens : undefined,
    maxTokens: typeof maxTokens === "number" ? maxTokens : undefined,
  };
}

/** Returns true if the TaskState is a terminal/final state. */
function isFinalState(state: TaskState): boolean {
  return (
    state === TaskState.TASK_STATE_COMPLETED ||
    state === TaskState.TASK_STATE_CANCELED ||
    state === TaskState.TASK_STATE_FAILED ||
    state === TaskState.TASK_STATE_REJECTED
  );
}

export async function runAgentStream(
  agentId: string,
  params: {
    prompt: string;
    runDir: string;
    threadId: string;
    runId: string;
    userMsgId?: string;
    model?: string;
  },
  callbacks: StreamCallbacks,
) {
  try {
    const client = await getAgentClient(agentId);

    const sendParams = {
      tenant: "",
      metadata: {
        run_dir: params.runDir,
        ...(params.model ? { model: params.model } : {}),
      },
      message: {
        messageId: params.userMsgId || "",
        contextId: params.threadId,
        role: Role.ROLE_USER,
        parts: [
          {
            content: { $case: "text" as const, value: params.prompt },
            mediaType: "text/plain",
            filename: "",
            metadata: undefined,
          },
        ],
        metadata: undefined,
        extensions: [],
        referenceTaskIds: [],
        taskId: "",
      },
      configuration: {
        acceptedOutputModes: ["text"],
        returnImmediately: false,
        taskPushNotificationConfig: undefined,
      },
    };

    const stream = client.sendMessageStream(sendParams);

    let accumulatedText = "";

    for await (const event of stream) {
      const payload = event.payload;
      if (!payload) continue;

      // Direct message from agent (no task wrapping)
      if (payload.$case === "message") {
        const msg = payload.value;
        const textContent = extractTextFromParts(msg.parts);
        if (textContent) {
          accumulatedText = textContent;
          const tokens = extractTokens(msg);
          callbacks.onText(accumulatedText, tokens.inputTokens, tokens.maxTokens, msg.metadata);
        }
        continue;
      }

      // Task snapshot (initial submission or final state)
      if (payload.$case === "task") {
        const task = payload.value;
        const status = task.status;
        if (!status) continue;

        const state = status.state;

        const msg = status.message;
        const entryType: string =
          task.metadata?.["entry_type"] ?? msg?.metadata?.["entry_type"] ?? "";

        if (msg) {
          const statusText = extractTextFromParts(msg.parts);
          if (statusText) {
            if (isFinalState(state)) {
              accumulatedText = statusText;
              const tokens = extractTokens(task);
              callbacks.onText(
                accumulatedText,
                tokens.inputTokens,
                tokens.maxTokens,
                task.metadata || msg?.metadata,
              );
            } else if (state !== TaskState.TASK_STATE_SUBMITTED) {
              callbacks.onStatus?.(statusText, entryType, TaskState[state]);
            }
          }
        }
        continue;
      }

      // Incremental status update (working, completed, etc.)
      if (payload.$case === "statusUpdate") {
        const update = payload.value;
        const status = update.status;
        if (!status) continue;

        const state = status.state;
        const msg = status.message;

        // entry_type is carried in metadata at event or message level
        const entryType: string =
          update.metadata?.["entry_type"] ?? msg?.metadata?.["entry_type"] ?? "";

        let statusText = "";
        if (msg?.parts) {
          statusText = extractTextFromParts(msg.parts);
        }

        if (!statusText) continue;

        const isAgentResponse = entryType === "agent_response";
        const isFinalResult = isFinalState(state) && !entryType;

        const isAppend =
          update.metadata?.["is_append"] === true || msg?.metadata?.["is_append"] === true;

        if (isAgentResponse || isFinalResult) {
          // Agent response text → assistant bubble
          if (isAppend) {
            accumulatedText += statusText;
          } else {
            accumulatedText = statusText;
          }
          const tokens = extractTokens(update);
          callbacks.onText(
            accumulatedText,
            tokens.inputTokens,
            tokens.maxTokens,
            update.metadata || msg?.metadata,
          );
        } else {
          // Tool calls, steps, reasoning, ask_user → status handler
          callbacks.onStatus?.(
            statusText,
            entryType,
            TaskState[state],
            update.metadata || msg?.metadata,
          );
        }
        continue;
      }
    }

    callbacks.onComplete?.();
  } catch (err: any) {
    console.error("[agent.ts] Stream error:", err);
    callbacks.onError?.(err instanceof Error ? err : new Error(String(err)));
  }
}
