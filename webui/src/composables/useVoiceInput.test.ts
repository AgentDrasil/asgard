// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useVoiceInput } from "./useVoiceInput";
import type { VoiceErrorCode } from "../types";
import * as api from "../lib/api";
import { AudioRecorder } from "../lib/audioRecorder";
import { GeminiLiveTranscribeClient } from "../lib/geminiLiveTranscribe";

vi.mock("../lib/api");
vi.mock("../lib/audioRecorder");
vi.mock("../lib/geminiLiveTranscribe", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/geminiLiveTranscribe")>();
  return {
    ...actual,
    GeminiLiveTranscribeClient: vi.fn<(...args: any[]) => any>(),
  };
});

describe("useVoiceInput", () => {
  let mockGetVoiceToken: any;
  let mockRecorderInstance: any;
  let liveClients: any[] = [];

  beforeEach(() => {
    vi.useFakeTimers();
    liveClients = [];

    mockGetVoiceToken = vi.spyOn(api, "getVoiceToken").mockResolvedValue({
      token: "test-ephemeral-token",
      expireTime: "2026-09-02T18:00:00Z",
      model: "models/gemini-3.5-transcribe-live",
    });

    mockRecorderInstance = {
      start: vi.fn<() => Promise<void>>().mockResolvedValue(undefined),
      stop: vi.fn<() => Promise<void>>().mockResolvedValue(undefined),
      getState: vi.fn<() => "idle" | "recording" | "stopped">().mockReturnValue("recording"),
      callbacks: null as any,
    };
    vi.mocked(AudioRecorder).mockImplementation(function (this: any, callbacks: any) {
      mockRecorderInstance.callbacks = callbacks;
      return mockRecorderInstance;
    });

    vi.mocked(GeminiLiveTranscribeClient).mockImplementation(function (
      this: any,
      callbacks: any,
      options: any,
    ) {
      const client = {
        connect: vi.fn<(token: string) => Promise<void>>().mockResolvedValue(undefined),
        sendAudioChunk: vi.fn<(chunk: ArrayBuffer) => void>(),
        sendAudioStreamEnd: vi.fn<() => void>(),
        close: vi.fn<() => void>(),
        callbacks,
        options,
      };
      liveClients.push(client);
      return client;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function getLastClient() {
    return liveClients[liveClients.length - 1];
  }

  it("should orchestrate start and stop recording transitions", async () => {
    const onFinalText = vi.fn<(text: string) => void>();
    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onFinalText, onError });

    expect(voice.state.value).toBe("idle");
    expect(voice.isRecording.value).toBe(false);
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.isStopping.value).toBe(false);

    const startPromise = voice.startRecording();
    expect(voice.isConnecting.value).toBe(true);
    expect(voice.state.value).toBe("connecting");

    await startPromise;

    const client = getLastClient();
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.isRecording.value).toBe(true);
    expect(voice.state.value).toBe("recording");
    expect(mockGetVoiceToken).toHaveBeenCalled();
    expect(client.connect).toHaveBeenCalledWith("test-ephemeral-token");
    expect(mockRecorderInstance.start).toHaveBeenCalled();

    // Now stop recording
    await voice.stopRecording();
    expect(voice.isRecording.value).toBe(false);
    expect(voice.isStopping.value).toBe(true);
    expect(voice.state.value).toBe("stopping");
    expect(mockRecorderInstance.stop).toHaveBeenCalled();
    expect(client.sendAudioStreamEnd).toHaveBeenCalled();

    // Simulate final event during stopping
    client.callbacks.onTranscription({
      type: "final",
      text: "Completed sentence.",
    });

    expect(voice.isStopping.value).toBe(false);
    expect(voice.state.value).toBe("idle");
    expect(onFinalText).toHaveBeenCalledWith("Completed sentence.");
  });

  it("should replace interim with final and assemble committed segments without duplication", async () => {
    const onFinalText = vi.fn<(text: string) => void>();
    const voice = useVoiceInput({ onFinalText });

    await voice.startRecording();
    const client = getLastClient();

    // 1. First interim
    client.callbacks.onTranscription({
      type: "interim",
      text: "First",
    });
    expect(voice.interimText.value).toBe("First");
    expect(voice.livePreviewText.value).toBe("First");

    // 2. Second interim
    client.callbacks.onTranscription({
      type: "interim",
      text: "First sentence",
    });
    expect(voice.interimText.value).toBe("First sentence");
    expect(voice.livePreviewText.value).toBe("First sentence");

    // 3. First final arrives
    client.callbacks.onTranscription({
      type: "final",
      text: "First sentence.",
    });
    expect(voice.interimText.value).toBe("");
    expect(voice.livePreviewText.value).toBe("First sentence.");

    // 4. Second sentence interim
    client.callbacks.onTranscription({
      type: "interim",
      text: "Second",
    });
    expect(voice.interimText.value).toBe("Second");
    expect(voice.livePreviewText.value).toBe("First sentence. Second");

    // 5. Second final arrives
    client.callbacks.onTranscription({
      type: "final",
      text: "Second sentence.",
    });
    expect(voice.interimText.value).toBe("");
    expect(voice.livePreviewText.value).toBe("First sentence. Second sentence.");

    // Stop recording and finish
    await voice.stopRecording();
    // Simulate final response on stop
    client.callbacks.onClose();

    expect(onFinalText).toHaveBeenCalledWith("First sentence. Second sentence.");
  });

  it("should commit last interim as fallback in multi-segment speech when timeout fires before last final (C6)", async () => {
    const onFinalText = vi.fn<(text: string) => void>();
    const voice = useVoiceInput({ onFinalText }, { finalizationTimeoutMs: 2500 });

    await voice.startRecording();
    const client = getLastClient();

    // Segment 1 committed as final
    client.callbacks.onTranscription({
      type: "final",
      text: "Hello",
    });
    expect(voice.livePreviewText.value).toBe("Hello");

    // Segment 2 only receives interim, never final
    client.callbacks.onTranscription({
      type: "interim",
      text: "world",
    });
    expect(voice.livePreviewText.value).toBe("Hello world");

    // User stops recording
    await voice.stopRecording();
    expect(voice.isStopping.value).toBe(true);

    // Timeout triggers (2.5 seconds fallback)
    await vi.advanceTimersByTimeAsync(2500);

    // Should include both the previously committed "Hello" AND the fallback interim "world"
    expect(onFinalText).toHaveBeenCalledWith("Hello world");
    expect(voice.isStopping.value).toBe(false);
    expect(voice.state.value).toBe("idle");
  });

  it("should transition to error state and release resources when startup fails (M4, M7)", async () => {
    // 1. Failure getting voice token
    mockGetVoiceToken.mockRejectedValueOnce(new Error("Token server error"));
    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onError });

    await voice.startRecording();

    expect(voice.error.value).toBe("voiceUnavailable");
    expect(voice.state.value).toBe("error");
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.isRecording.value).toBe(false);
    expect(onError).toHaveBeenCalledWith("voiceUnavailable");

    // 2. Failure starting audio recorder (mic denied)
    mockGetVoiceToken.mockResolvedValueOnce({
      token: "valid-token",
      expireTime: "2026-09-02T18:00:00Z",
      model: "models/gemini-3.5-transcribe-live",
    });
    mockRecorderInstance.start.mockRejectedValueOnce(new Error("Permission denied"));

    await voice.startRecording();

    const client = getLastClient();
    expect(voice.error.value).toBe("micDenied");
    expect(voice.state.value).toBe("error");
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.isRecording.value).toBe(false);
    expect(onError).toHaveBeenCalledWith("micDenied");
    // Verify client was closed and cleaned up
    expect(client.close).toHaveBeenCalled();
  });

  it("should handle session timeout and network errors during active recording", async () => {
    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onError });

    await voice.startRecording();
    expect(voice.isRecording.value).toBe(true);

    const client = getLastClient();
    // Simulate session timeout error from client
    client.callbacks.onError({
      message: "Session maximum duration reached (10 minutes)",
      isTimeout: true,
    });

    expect(voice.error.value).toBe("sessionTimeout");
    expect(onError).toHaveBeenCalledWith("sessionTimeout");
  });

  it("should commit pending interim when websocket drops unexpectedly (D2, C7)", async () => {
    const onFinalText = vi.fn<(text: string) => void>();
    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onFinalText, onError });

    await voice.startRecording();
    const client = getLastClient();

    // 1. Confirmed final segment arrives
    client.callbacks.onTranscription({
      type: "final",
      text: "Hello",
    });

    // 2. Interim segment arrives
    client.callbacks.onTranscription({
      type: "interim",
      text: "world",
    });

    expect(voice.livePreviewText.value).toBe("Hello world");
    expect(voice.isRecording.value).toBe(true);

    // 3. WebSocket drops unexpectedly while still recording (not stopping)
    client.callbacks.onClose();

    // Verify interim was settled into final text and error code was set
    expect(onFinalText).toHaveBeenCalledWith("Hello world");
    expect(voice.error.value).toBe("network");
    expect(onError).toHaveBeenCalledWith("network");
    expect(voice.isRecording.value).toBe(false);
    expect(voice.state.value).toBe("error");
  });

  it("should commit pending interim when 10-minute timeout fires (D2, C7)", async () => {
    const onFinalText = vi.fn<(text: string) => void>();
    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onFinalText, onError });

    await voice.startRecording();
    const client = getLastClient();

    // Interim speech underway
    client.callbacks.onTranscription({
      type: "interim",
      text: "final sentence before timeout",
    });

    // 10-minute timeout fires
    client.callbacks.onError({
      message: "Session maximum duration reached (10 minutes)",
      isTimeout: true,
    });

    expect(onFinalText).toHaveBeenCalledWith("final sentence before timeout");
    expect(voice.error.value).toBe("sessionTimeout");
    expect(onError).toHaveBeenCalledWith("sessionTimeout");
    expect(voice.isRecording.value).toBe(false);
    expect(voice.state.value).toBe("error");
  });

  it("should not surface error when cancel during connecting races a failing connect (D3)", async () => {
    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onError });

    let rejectConnect: (reason?: any) => void = () => {};
    // Mock connect to return a controllable pending promise
    vi.mocked(GeminiLiveTranscribeClient).mockImplementationOnce(function (
      this: any,
      callbacks: any,
      options: any,
    ) {
      const client = {
        connect: vi.fn<(token: string) => Promise<void>>().mockImplementation(
          () =>
            new Promise<void>((_, reject) => {
              rejectConnect = reject;
            }),
        ),
        sendAudioChunk: vi.fn<(chunk: ArrayBuffer) => void>(),
        sendAudioStreamEnd: vi.fn<() => void>(),
        close: vi.fn<() => void>(),
        callbacks,
        options,
      };
      liveClients.push(client);
      return client;
    });

    // 1. User starts recording -> in connecting state
    const startPromise = voice.startRecording();
    // Flush microtasks so token fetch resolves, liveClient is instantiated and connect() is pending
    await Promise.resolve();
    await Promise.resolve();
    expect(voice.isConnecting.value).toBe(true);
    const firstClient = getLastClient();

    // 2. User stops/cancels while connect is still pending
    await voice.stopRecording();
    expect(firstClient.close).toHaveBeenCalled();
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.state.value).toBe("idle");

    // 3. Pending connect promise fails / rejects after cancellation
    rejectConnect(new Error("Connect failed"));
    await startPromise;

    // No error should be surfaced to error ref or onError callback
    expect(voice.error.value).toBeNull();
    expect(onError).not.toHaveBeenCalled();
    expect(voice.state.value).toBe("idle");

    // 4. Subsequent recording flow works cleanly
    await voice.startRecording();
    expect(voice.isRecording.value).toBe(true);
    const nextClient = getLastClient();
    nextClient.callbacks.onTranscription({
      type: "final",
      text: "Normal retry works.",
    });
    await voice.stopRecording();
    nextClient.callbacks.onClose();
    expect(voice.isRecording.value).toBe(false);
    expect(voice.state.value).toBe("idle");
  });

  it("should treat stop during connecting as cancel and allow a clean restart", async () => {
    const onFinalText = vi.fn<(text: string) => void>();
    const voice = useVoiceInput({ onFinalText });

    // 1. Trigger startRecording, which puts it into connecting state
    const startPromise = voice.startRecording();
    expect(voice.isConnecting.value).toBe(true);
    expect(voice.state.value).toBe("connecting");

    // 2. User clicks stop while still connecting
    await voice.stopRecording();

    // Since it was connecting, stop should behave as cancel
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.isStopping.value).toBe(false);
    expect(voice.isRecording.value).toBe(false);
    expect(voice.state.value).toBe("idle");

    // Let any pending background promises in startRecording resolve
    await startPromise;

    // State should still be idle, no orphan recording started
    expect(voice.isRecording.value).toBe(false);
    expect(voice.state.value).toBe("idle");

    // 3. Clean restart: startRecording again
    await voice.startRecording();
    expect(voice.isRecording.value).toBe(true);
    expect(voice.state.value).toBe("recording");

    const client = getLastClient();
    // Simulate multi-segment speech
    client.callbacks.onTranscription({
      type: "final",
      text: "Restart works.",
    });

    // Normal stop
    await voice.stopRecording();
    expect(voice.isStopping.value).toBe(true);
    expect(voice.state.value).toBe("stopping");

    // Simulate close on stop
    client.callbacks.onClose();

    expect(voice.isStopping.value).toBe(false);
    expect(voice.state.value).toBe("idle");
    expect(onFinalText).toHaveBeenCalledWith("Restart works.");
  });

  it("should not corrupt a restarted session when a cancelled start flow resumes late (D5)", async () => {
    let resolveRecorderStartA: () => void = () => {};
    let recorderStartCallCount = 0;

    // First recorder instance hangs on start until explicitly released
    mockRecorderInstance.start.mockImplementation(() => {
      recorderStartCallCount++;
      if (recorderStartCallCount === 1) {
        return new Promise<void>((resolve) => {
          resolveRecorderStartA = resolve;
        });
      }
      return Promise.resolve();
    });

    const onFinalText = vi.fn<(text: string) => void>();
    const voice = useVoiceInput({ onFinalText });

    // 1. User starts session A -> connecting
    const startPromiseA = voice.startRecording();
    await Promise.resolve();
    await Promise.resolve();
    expect(voice.isConnecting.value).toBe(true);
    const clientA = getLastClient();

    // 2. User cancels session A while recorder.start is pending
    await voice.stopRecording();
    expect(voice.isConnecting.value).toBe(false);
    expect(voice.state.value).toBe("idle");
    // Client A is closed during cancel
    expect(clientA.close).toHaveBeenCalled();

    // 3. User immediately starts session B before flow A completes
    const startPromiseB = voice.startRecording();
    await startPromiseB;

    expect(voice.isRecording.value).toBe(true);
    expect(voice.state.value).toBe("recording");
    const clientB = getLastClient();
    expect(clientB).not.toBe(clientA);
    expect(clientB.close).not.toHaveBeenCalled();

    // 4. Now flow A finally resolves late
    resolveRecorderStartA();
    await startPromiseA;

    // Session B must NOT be corrupted:
    // clientB must not be closed, recorder must not be stopped, state must still be recording
    expect(clientB.close).not.toHaveBeenCalled();
    expect(voice.isRecording.value).toBe(true);
    expect(voice.state.value).toBe("recording");

    // Session B transcribes speech normally
    clientB.callbacks.onTranscription({
      type: "final",
      text: "Session B intact.",
    });

    await voice.stopRecording();
    clientB.callbacks.onClose();

    expect(voice.isRecording.value).toBe(false);
    expect(voice.state.value).toBe("idle");
    expect(onFinalText).toHaveBeenCalledWith("Session B intact.");
  });

  it("should not leak mic or surface error when superseded flow resumes after restart (D5)", async () => {
    let resolveConnectA: () => void = () => {};
    let connectCallCount = 0;

    vi.mocked(GeminiLiveTranscribeClient).mockImplementation(function (
      this: any,
      callbacks: any,
      options: any,
    ) {
      connectCallCount++;
      const currentCall = connectCallCount;
      const client = {
        connect: vi.fn<(token: string) => Promise<void>>().mockImplementation(() => {
          if (currentCall === 1) {
            return new Promise<void>((resolve) => {
              resolveConnectA = resolve;
            });
          }
          return Promise.resolve();
        }),
        sendAudioChunk: vi.fn<(chunk: ArrayBuffer) => void>(),
        sendAudioStreamEnd: vi.fn<() => void>(),
        close: vi.fn<() => void>(),
        callbacks,
        options,
      };
      liveClients.push(client);
      return client;
    });

    const onError = vi.fn<(err: VoiceErrorCode) => void>();
    const voice = useVoiceInput({ onError });

    // 1. Session A starts and hangs in connect()
    const startPromiseA = voice.startRecording();
    await Promise.resolve();
    await Promise.resolve();
    expect(voice.isConnecting.value).toBe(true);
    const clientA = liveClients[liveClients.length - 1];

    // 2. User cancels during connecting
    voice.cancelRecording();
    expect(clientA.close).toHaveBeenCalled();
    expect(voice.isConnecting.value).toBe(false);

    // 3. User immediately restarts session B
    const startPromiseB = voice.startRecording();
    await startPromiseB;
    expect(voice.isRecording.value).toBe(true);

    // 4. Session A's connect completes late
    resolveConnectA();
    await startPromiseA;

    // Superseded flow A must not touch error or destroy session B
    expect(voice.error.value).toBeNull();
    expect(onError).not.toHaveBeenCalled();
    expect(voice.isRecording.value).toBe(true);
    expect(voice.state.value).toBe("recording");
  });
});
