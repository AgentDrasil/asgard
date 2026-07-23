import {
  ClientFactory,
  RestTransportFactory,
  JsonRpcTransportFactory,
  Client,
} from "@a2a-js/sdk/client";
import { apiFetch } from "./api";

interface NewSpecAgentCard {
  supportedInterfaces?: Array<{ url: string; protocolBinding: string }>;
  [key: string]: unknown;
}

// Translate from the Go server's supportedInterfaces spec to the SDK's expected format.
async function fetchAndBridgeAgentCard(cardUrl: string): Promise<object> {
  const res = await apiFetch(cardUrl);
  if (!res.ok) {
    throw new Error(`Failed to fetch agent card: ${res.status} ${res.statusText}`);
  }
  const card = (await res.json()) as NewSpecAgentCard;

  if ("url" in card) {
    return card;
  }

  const ifaces = card.supportedInterfaces ?? [];
  if (ifaces.length === 0) {
    throw new Error("Agent card has no supportedInterfaces");
  }

  const [primary, ...rest] = ifaces;
  return {
    ...card,
    url: primary.url,
    preferredTransport: primary.protocolBinding,
    additionalInterfaces: rest.map((i) => ({ url: i.url, transport: i.protocolBinding })),
  };
}

// Patched fetch to bridge the Go server's taskId field to the SDK's expected taskStatusUpdateEvent name field.
async function patchedFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const response = await apiFetch(input, init);
  const contentType = response.headers.get("Content-Type");
  if (!contentType?.startsWith("text/event-stream") || !response.body) {
    return response;
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  const encoder = new TextEncoder();
  let buffer = "";

  const stream = new ReadableStream({
    async start(controller) {
      try {
        while (true) {
          const { done, value } = await reader.read();
          if (done) {
            if (buffer) {
              controller.enqueue(encoder.encode(buffer));
            }
            controller.close();
            break;
          }

          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split("\n");
          buffer = lines.pop() || "";

          for (let line of lines) {
            if (line.startsWith("data: ")) {
              try {
                const jsonStr = line.slice(6).trim();
                if (jsonStr) {
                  const obj = JSON.parse(jsonStr);

                  // Helper to recursively map parts -> content for the SDK parser
                  const translateParts = (o: any) => {
                    if (o && typeof o === "object") {
                      if (Array.isArray(o.parts) && o.content === undefined) {
                        o.content = o.parts;
                      }
                      for (const k of Object.keys(o)) {
                        translateParts(o[k]);
                      }
                    }
                  };
                  translateParts(obj);

                  if (obj.statusUpdate && obj.statusUpdate.taskId && !obj.statusUpdate.name) {
                    obj.statusUpdate.name = `tasks/${obj.statusUpdate.taskId}`;
                  }

                  line = `data: ${JSON.stringify(obj)}`;
                }
              } catch (e) {
                console.error("[patchedFetch] Failed to parse JSON:", line, e);
              }
            }
            controller.enqueue(encoder.encode(line + "\n"));
          }
        }
      } catch (err) {
        console.error("[patchedFetch] Stream error:", err);
        controller.error(err);
      }
    },
  });

  return new Response(stream, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}

// Keep a cache of client instances to avoid fetching agent-card every request
const clientCache: Record<string, Client> = {};

export async function getAgentClient(agentId: string, customBaseUrl?: string): Promise<Client> {
  if (clientCache[agentId]) {
    return clientCache[agentId];
  }

  const baseUrl = customBaseUrl || window.location.origin;
  const endpoint = `${baseUrl}/agents/${agentId}/`;
  const cardUrl = `${endpoint}.well-known/agent-card.json`;

  const factory = new ClientFactory({
    transports: [
      new RestTransportFactory({ fetchImpl: patchedFetch }),
      new JsonRpcTransportFactory(),
    ],
    preferredTransports: ["HTTP+JSON"],
  });

  const card = await fetchAndBridgeAgentCard(cardUrl);
  const client = await factory.createFromAgentCard(
    card as Parameters<typeof factory.createFromAgentCard>[0],
  );

  clientCache[agentId] = client;
  return client;
}

export interface StreamCallbacks {
  onText: (text: string) => void;
  onReasoning?: (text: string) => void;
  onStatus?: (statusText: string, state?: string) => void;
  onError?: (err: Error) => void;
  onComplete?: () => void;
}

export async function runAgentStream(
  agentId: string,
  params: {
    prompt: string;
    runDir: string;
    threadId: string;
    runId: string;
    userMsgId: string;
  },
  callbacks: StreamCallbacks,
) {
  try {
    const client = await getAgentClient(agentId);

    const sendParams = {
      message: {
        kind: "message" as const,
        messageId: params.userMsgId,
        contextId: params.threadId,
        role: "user" as const,
        parts: [
          {
            kind: "text" as const,
            text: params.prompt,
          },
        ],
      },
      configuration: {
        acceptedOutputModes: ["text"],
        state: {
          run_dir: params.runDir,
        },
      },
    };

    console.log("[agent.ts] Sending message stream with parameters:", sendParams);
    const stream = client.sendMessageStream(sendParams);

    let accumulatedText = "";

    for await (const event of stream) {
      const eventAny = event as any;
      const eventKind: string = eventAny.kind ?? "";
      const eventStatus = eventAny.status;
      const eventParts = eventAny.parts;
      console.log(
        "[agent.ts] kind:",
        eventKind,
        "state:",
        eventStatus?.state,
        "final:",
        eventAny.final,
        "entry_type:",
        eventStatus?.message?.metadata?.entry_type,
      );

      // Handle raw message events (kind === "message")
      if (eventKind === "message" && eventParts) {
        let textContent = "";
        for (const part of eventParts) {
          if (part.kind === "text") textContent += part.text;
        }
        if (textContent) {
          accumulatedText += textContent;
          callbacks.onText(accumulatedText);
        }
      }

      // Handle status updates (kind === "status-update")
      if (eventKind === "status-update" && eventStatus) {
        const state: string = eventStatus.state ?? "";
        const msg = eventStatus.message;
        // entry_type is now set at event level (eventAny.metadata) and message level as fallback
        const entryType: string = eventAny.metadata?.entry_type ?? msg?.metadata?.entry_type ?? "";
        const isFinal =
          eventAny.final === true ||
          state === "completed" ||
          state === "canceled" ||
          state === "failed";

        let statusText = "";
        if (msg?.parts) {
          for (const part of msg.parts) {
            if (part.kind === "text") statusText += part.text;
            else if (part.part?.$case === "text") statusText += part.part.value;
          }
        }

        if (!statusText) continue;

        if (entryType === "agent_response" || isFinal) {
          // Agent response text (streaming or final) → assistant bubble
          accumulatedText += statusText;
          callbacks.onText(accumulatedText);
        } else {
          // Tool calls, steps, reasoning → thinking/activity box
          callbacks.onStatus?.(statusText, state);
        }
      }
    }

    callbacks.onComplete?.();
  } catch (err: any) {
    console.error("[agent.ts] Stream error:", err);
    callbacks.onError?.(err instanceof Error ? err : new Error(String(err)));
  }
}
