import { ref, computed } from "vue";
import type { VoiceErrorCode, VoiceInputState } from "../types";
import { getVoiceToken } from "../lib/api";
import { AudioRecorder } from "../lib/audioRecorder";
import {
  GeminiLiveTranscribeClient,
  FINALIZATION_TIMEOUT_MS,
  MAX_SESSION_DURATION_MS,
} from "../lib/geminiLiveTranscribe";

export interface UseVoiceInputCallbacks {
  onFinalText?: (text: string) => void;
  onError?: (errorCode: VoiceErrorCode) => void;
}

export interface UseVoiceInputOptions {
  wsBaseUrl?: string;
  finalizationTimeoutMs?: number;
  maxSessionDurationMs?: number;
}

export function useVoiceInput(callbacks?: UseVoiceInputCallbacks, options?: UseVoiceInputOptions) {
  const isRecording = ref(false);
  const isConnecting = ref(false);
  const isStopping = ref(false);
  const interimText = ref("");
  const error = ref<VoiceErrorCode | null>(null);

  const committedFinals = ref<string[]>([]);
  const currentInterim = ref("");

  let sessionEpoch = 0;
  let pendingClient: GeminiLiveTranscribeClient | null = null;
  let recorder: AudioRecorder | null = null;
  let liveClient: GeminiLiveTranscribeClient | null = null;
  let finalizeTimerId: ReturnType<typeof setTimeout> | null = null;
  let hasFinalized = false;

  const livePreviewText = computed(() => {
    const parts: string[] = [];
    if (committedFinals.value.length > 0) {
      parts.push(committedFinals.value.join(" "));
    }
    if (interimText.value.trim().length > 0) {
      parts.push(interimText.value.trim());
    }
    return parts.join(" ").trim();
  });

  const state = computed<VoiceInputState>(() => {
    if (error.value) return "error";
    if (isStopping.value) return "stopping";
    if (isRecording.value) return "recording";
    if (isConnecting.value) return "connecting";
    return "idle";
  });

  function clearFinalizeTimer() {
    if (finalizeTimerId) {
      clearTimeout(finalizeTimerId);
      finalizeTimerId = null;
    }
  }

  function commitAndFinish() {
    if (hasFinalized) return;
    hasFinalized = true;
    clearFinalizeTimer();

    // C6: All-scenario fallback - if currentInterim exists, push into committedFinals
    // regardless of whether committedFinals already has prior segments
    if (currentInterim.value.trim().length > 0) {
      committedFinals.value.push(currentInterim.value.trim());
      currentInterim.value = "";
      interimText.value = "";
    }

    const fullText = committedFinals.value.join(" ").trim();
    if (fullText.length > 0) {
      callbacks?.onFinalText?.(fullText);
    }

    if (recorder) {
      try {
        recorder.stop();
      } catch {
        // ignore
      }
      recorder = null;
    }

    if (liveClient) {
      liveClient.close();
      liveClient = null;
    }

    isStopping.value = false;
    committedFinals.value = [];
    currentInterim.value = "";
    interimText.value = "";
  }

  function setError(code: VoiceErrorCode) {
    error.value = code;
    callbacks?.onError?.(code);
  }

  function abortAndSettle(code: VoiceErrorCode): void {
    if (isStopping.value) {
      commitAndFinish();
    } else if (isRecording.value) {
      isRecording.value = false;
      commitAndFinish();
    } else {
      cancelRecording();
      setError(code);
      return;
    }
    setError(code);
  }

  async function startRecording(): Promise<void> {
    if (isRecording.value || isConnecting.value || isStopping.value) {
      return;
    }

    const epoch = ++sessionEpoch;
    error.value = null;
    isConnecting.value = true;
    committedFinals.value = [];
    currentInterim.value = "";
    interimText.value = "";
    hasFinalized = false;
    clearFinalizeTimer();

    let token = "";
    try {
      const tokenResp = await getVoiceToken();
      if (epoch !== sessionEpoch) {
        return;
      }
      token = tokenResp.token;
      if (!token) {
        throw new Error("Empty voice token returned from server");
      }
    } catch {
      if (epoch !== sessionEpoch) {
        return;
      }
      isConnecting.value = false;
      setError("voiceUnavailable");
      return;
    }

    if (epoch !== sessionEpoch) {
      return;
    }

    const client = new GeminiLiveTranscribeClient(
      {
        onTranscription: (event) => {
          if (epoch !== sessionEpoch) {
            return;
          }
          if (event.type === "interim") {
            currentInterim.value = event.text;
            interimText.value = event.text;
          } else if (event.type === "final") {
            const trimmed = event.text.trim();
            if (trimmed.length > 0) {
              committedFinals.value.push(trimmed);
            }
            currentInterim.value = "";
            interimText.value = "";

            // If we are already in stopping state and received a final transcription,
            // we can complete immediately
            if (isStopping.value) {
              commitAndFinish();
            }
          }
        },
        onError: (err) => {
          if (epoch !== sessionEpoch) {
            return;
          }
          const isTimeout =
            (err as { isTimeout?: boolean }).isTimeout || err.message.includes("10 minutes");
          abortAndSettle(isTimeout ? "sessionTimeout" : "network");
        },
        onClose: () => {
          if (epoch !== sessionEpoch) {
            return;
          }
          if (isStopping.value) {
            commitAndFinish();
          } else if (isRecording.value) {
            abortAndSettle("network");
          }
        },
      },
      {
        baseUrl: options?.wsBaseUrl,
        maxDurationMs: options?.maxSessionDurationMs ?? MAX_SESSION_DURATION_MS,
      },
    );

    pendingClient = client;

    try {
      await client.connect(token);
    } catch {
      client.close();
      if (pendingClient === client) {
        pendingClient = null;
      }
      if (epoch !== sessionEpoch) {
        return;
      }
      isConnecting.value = false;
      setError("network");
      return;
    }

    if (epoch !== sessionEpoch) {
      client.close();
      if (pendingClient === client) {
        pendingClient = null;
      }
      return;
    }

    let rec: AudioRecorder;
    try {
      rec = new AudioRecorder({
        onData: (chunk) => {
          if (epoch !== sessionEpoch) {
            return;
          }
          if (liveClient && (isRecording.value || isStopping.value)) {
            liveClient.sendAudioChunk(chunk);
          }
        },
        onError: (_recErr) => {
          if (epoch !== sessionEpoch) {
            return;
          }
          console.error("AudioRecorder error:", _recErr);
          setError("micDenied");
          cancelRecording();
        },
      });

      await rec.start();
    } catch {
      try {
        await rec!.stop();
      } catch {
        // ignore
      }
      client.close();
      if (epoch !== sessionEpoch) {
        return;
      }
      isConnecting.value = false;
      setError("micDenied");
      return;
    }

    if (epoch !== sessionEpoch) {
      try {
        await rec.stop();
      } catch {
        // ignore
      }
      client.close();
      return;
    }

    recorder = rec;
    liveClient = client;
    if (pendingClient === client) {
      pendingClient = null;
    }
    isConnecting.value = false;
    isRecording.value = true;
  }

  async function stopRecording(): Promise<void> {
    if (isConnecting.value) {
      // Stop during connecting is a cancel: nothing to finalize yet.
      cancelRecording();
      return;
    }

    if (!isRecording.value) {
      return;
    }

    isRecording.value = false;
    isStopping.value = true;

    // 1. Immediately stop recorder to release mic hardware
    if (recorder) {
      try {
        await recorder.stop();
      } catch {
        // ignore
      }
      recorder = null;
    }

    // 2. Notify model about stream end
    if (liveClient) {
      try {
        liveClient.sendAudioStreamEnd();
      } catch {
        // ignore
      }
    }

    // 3. Start bounded fallback timer
    clearFinalizeTimer();
    const timeoutMs = options?.finalizationTimeoutMs ?? FINALIZATION_TIMEOUT_MS;
    finalizeTimerId = setTimeout(() => {
      commitAndFinish();
    }, timeoutMs);
  }

  function cancelRecording(): void {
    sessionEpoch++;
    isRecording.value = false;
    isConnecting.value = false;
    isStopping.value = false;
    clearFinalizeTimer();

    if (recorder) {
      try {
        recorder.stop();
      } catch {
        // ignore
      }
      recorder = null;
    }

    if (pendingClient) {
      pendingClient.close();
      pendingClient = null;
    }

    if (liveClient) {
      liveClient.close();
      liveClient = null;
    }

    committedFinals.value = [];
    currentInterim.value = "";
    interimText.value = "";
  }

  return {
    isRecording,
    isConnecting,
    isStopping,
    interimText,
    livePreviewText,
    error,
    state,
    startRecording,
    stopRecording,
    cancelRecording,
  };
}
