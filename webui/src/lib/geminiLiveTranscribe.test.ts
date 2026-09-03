// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  GeminiLiveTranscribeClient,
  GEMINI_LIVE_WS_BASE_URL,
  GEMINI_TRANSCRIBE_MODEL,
  MAX_SESSION_DURATION_MS,
} from "./geminiLiveTranscribe";
import { PRESET_VOICE_VOCABULARY } from "./voiceVocabulary";

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static OPEN = 1;
  static CONNECTING = 0;
  static CLOSING = 2;
  static CLOSED = 3;

  public url: string;
  public readyState: number = MockWebSocket.CONNECTING;
  public onopen: (() => void) | null = null;
  public onmessage: ((event: { data: any }) => void) | null = null;
  public onerror: ((event: any) => void) | null = null;
  public onclose: (() => void) | null = null;
  public sentMessages: string[] = [];

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
    // Simulate auto-open on next tick
    setTimeout(() => {
      if (this.readyState === MockWebSocket.CONNECTING) {
        this.readyState = MockWebSocket.OPEN;
        this.onopen?.();
      }
    }, 0);
  }

  send(data: string) {
    this.sentMessages.push(data);
  }

  close() {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.();
  }

  simulateMessage(data: any) {
    this.onmessage?.({ data: typeof data === "string" ? data : JSON.stringify(data) });
  }

  simulateError(err?: any) {
    this.onerror?.(err || new Error("Mock WS error"));
  }
}

describe("GeminiLiveTranscribeClient", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    MockWebSocket.instances = [];
    (globalThis as any).WebSocket = MockWebSocket;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should connect with ephemeral token as access_token query parameter (C5)", async () => {
    const client = new GeminiLiveTranscribeClient({});
    const token = "auth_tokens/test-token-123";
    const connectPromise = client.connect(token);

    expect(MockWebSocket.instances.length).toBe(1);
    const wsInstance = MockWebSocket.instances[0];
    expect(wsInstance.url).toBe(
      `${GEMINI_LIVE_WS_BASE_URL}?access_token=${encodeURIComponent(token)}`,
    );

    // Fast-forward 10ms to trigger onopen
    await vi.advanceTimersByTimeAsync(10);
    await connectPromise;

    client.close();
  });

  it("should send setup frame with customVocabulary (<=100 items) (C1)", async () => {
    const client = new GeminiLiveTranscribeClient({});
    const token = "test-token";
    const connectPromise = client.connect(token);

    await vi.advanceTimersByTimeAsync(10);
    await connectPromise;

    const wsInstance = MockWebSocket.instances[0];
    expect(wsInstance.sentMessages.length).toBeGreaterThan(0);

    const setupMsg = JSON.parse(wsInstance.sentMessages[0]);
    expect(setupMsg.setup).toBeDefined();
    expect(setupMsg.setup.model).toBe(GEMINI_TRANSCRIBE_MODEL);
    expect(setupMsg.setup.inputAudioTranscription?.mode).toBe("SMART");
    expect(setupMsg.setup.inputAudioTranscription?.customVocabulary).toBeDefined();
    expect(Array.isArray(setupMsg.setup.inputAudioTranscription?.customVocabulary)).toBe(true);
    expect(setupMsg.setup.inputAudioTranscription?.customVocabulary.length).toBeLessThanOrEqual(
      100,
    );
    expect(setupMsg.setup.inputAudioTranscription?.customVocabulary).toContain("Asgard");
    expect(setupMsg.setup.inputAudioTranscription?.customVocabulary).toEqual(
      PRESET_VOICE_VOCABULARY,
    );

    client.close();
  });

  it("should stream audio chunks as base64", async () => {
    const client = new GeminiLiveTranscribeClient({});
    const connectPromise = client.connect("token");
    await vi.advanceTimersByTimeAsync(10);
    await connectPromise;

    const wsInstance = MockWebSocket.instances[0];
    const buffer = new Uint8Array([1, 2, 3, 4]).buffer;
    client.sendAudioChunk(buffer);

    expect(wsInstance.sentMessages.length).toBe(2);
    const audioMsg = JSON.parse(wsInstance.sentMessages[1]);
    expect(audioMsg.realtimeInput?.mediaChunks?.[0]?.mimeType).toBe("audio/pcm;rate=16000");
    expect(typeof audioMsg.realtimeInput?.mediaChunks?.[0]?.data).toBe("string");

    client.close();
  });

  it("should send audioStreamEnd true", async () => {
    const client = new GeminiLiveTranscribeClient({});
    const connectPromise = client.connect("token");
    await vi.advanceTimersByTimeAsync(10);
    await connectPromise;

    const wsInstance = MockWebSocket.instances[0];
    client.sendAudioStreamEnd();

    expect(wsInstance.sentMessages.length).toBe(2);
    const endMsg = JSON.parse(wsInstance.sentMessages[1]);
    expect(endMsg.realtimeInput?.audioStreamEnd).toBe(true);

    client.close();
  });

  it("should parse interim and final transcription server messages (both snake_case and camelCase)", async () => {
    const events: any[] = [];
    const client = new GeminiLiveTranscribeClient({
      onTranscription: (event) => {
        events.push(event);
      },
    });

    const connectPromise = client.connect("token");
    await vi.advanceTimersByTimeAsync(10);
    await connectPromise;

    const wsInstance = MockWebSocket.instances[0];

    // 1. Simulate serverContent.interim_input_transcription (snake_case)
    wsInstance.simulateMessage({
      serverContent: {
        interim_input_transcription: {
          text: "Hello snake",
        },
      },
    });

    // 2. Simulate serverContent.input_transcription (snake_case final)
    wsInstance.simulateMessage({
      serverContent: {
        input_transcription: {
          text: "Hello snake world",
        },
      },
    });

    // 3. Simulate camelCase
    wsInstance.simulateMessage({
      serverContent: {
        interimInputTranscription: {
          text: "Hello camel",
        },
      },
    });
    wsInstance.simulateMessage({
      serverContent: {
        inputTranscription: {
          text: "Hello camel world",
        },
      },
    });

    expect(events).toEqual([
      { type: "interim", text: "Hello snake" },
      { type: "final", text: "Hello snake world" },
      { type: "interim", text: "Hello camel" },
      { type: "final", text: "Hello camel world" },
    ]);

    client.close();
  });

  it("should enforce 10-minute session duration timeout", async () => {
    const onError = vi.fn<(error: Error) => void>();
    const client = new GeminiLiveTranscribeClient({ onError });

    const connectPromise = client.connect("token");
    await vi.advanceTimersByTimeAsync(10);
    await connectPromise;

    const wsInstance = MockWebSocket.instances[0];
    expect(wsInstance.readyState).toBe(MockWebSocket.OPEN);

    // Fast forward 10 minutes
    await vi.advanceTimersByTimeAsync(MAX_SESSION_DURATION_MS);

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("10 minutes"),
      }),
    );
    expect(wsInstance.readyState).toBe(MockWebSocket.CLOSED);
  });

  it("should suppress onError callback after client is explicitly closed (D3)", async () => {
    const onError = vi.fn<(error: Error) => void>();
    const client = new GeminiLiveTranscribeClient({ onError });

    const connectPromise = client.connect("token");
    const wsInstance = MockWebSocket.instances[0];

    // Explicitly close before or while connecting
    client.close();

    // WS fires error during close
    wsInstance.simulateError(new Error("WS aborted"));

    // connect promise should still reject because connected was false
    await expect(connectPromise).rejects.toThrow(
      "WebSocket closed before connection was established",
    );

    // But callbacks.onError must NOT be invoked
    expect(onError).not.toHaveBeenCalled();
  });
});
