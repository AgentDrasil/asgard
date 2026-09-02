import type { TranscriptionEvent } from "../types";
import { PRESET_VOICE_VOCABULARY } from "./voiceVocabulary";

export const GEMINI_LIVE_WS_BASE_URL =
  "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateContentConstrained";
export const GEMINI_TRANSCRIBE_MODEL = "models/gemini-3.5-transcribe-live";
export const MAX_SESSION_DURATION_MS = 10 * 60 * 1000; // 10 minutes
export const FINALIZATION_TIMEOUT_MS = 2500; // 2.5s fallback

export interface GeminiLiveTranscribeOptions {
  baseUrl?: string;
  model?: string;
  vocabulary?: string[];
  maxDurationMs?: number;
}

export interface GeminiLiveTranscribeCallbacks {
  onTranscription?: (event: TranscriptionEvent) => void;
  onError?: (error: Error) => void;
  onClose?: () => void;
}

function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  const len = bytes.byteLength;
  for (let i = 0; i < len; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

export class GeminiLiveTranscribeClient {
  private ws: WebSocket | null = null;
  private callbacks: GeminiLiveTranscribeCallbacks;
  private options: GeminiLiveTranscribeOptions;
  private sessionTimeoutId: ReturnType<typeof setTimeout> | null = null;
  private isExplicitlyClosed = false;

  constructor(callbacks: GeminiLiveTranscribeCallbacks, options?: GeminiLiveTranscribeOptions) {
    this.callbacks = callbacks;
    this.options = options || {};
  }

  public connect(token: string): Promise<void> {
    return new Promise((resolve, reject) => {
      this.isExplicitlyClosed = false;
      const baseUrl = this.options.baseUrl || GEMINI_LIVE_WS_BASE_URL;
      const wsUrl = `${baseUrl}?key=${encodeURIComponent(token)}`;

      try {
        const WebSocketClass =
          typeof WebSocket !== "undefined"
            ? WebSocket
            : (globalThis as unknown as { WebSocket?: typeof WebSocket }).WebSocket;

        if (!WebSocketClass) {
          throw new Error("WebSocket is not supported in this environment");
        }

        this.ws = new WebSocketClass(wsUrl);
      } catch (err: unknown) {
        const error = err instanceof Error ? err : new Error(String(err));
        this.callbacks.onError?.(error);
        return reject(error);
      }

      let connected = false;

      this.ws.onopen = () => {
        connected = true;
        this.sendSetup();
        this.scheduleSessionTimeout();
        resolve();
      };

      this.ws.onerror = (_event: Event) => {
        const err = new Error("WebSocket encountered an error");
        if (!connected) {
          reject(err);
        }
        if (!this.isExplicitlyClosed) {
          this.callbacks.onError?.(err);
        }
      };

      this.ws.onclose = () => {
        this.clearSessionTimeout();
        if (!connected) {
          reject(new Error("WebSocket closed before connection was established"));
        }
        if (!this.isExplicitlyClosed) {
          this.callbacks.onClose?.();
        }
      };

      this.ws.onmessage = (event: MessageEvent) => {
        this.handleMessage(event.data);
      };
    });
  }

  private sendSetup(): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const vocabulary = this.options.vocabulary ?? PRESET_VOICE_VOCABULARY;
    const model = this.options.model ?? GEMINI_TRANSCRIBE_MODEL;

    const setupMsg = {
      setup: {
        model,
        generationConfig: {
          responseModalities: ["TEXT"],
        },
        inputAudioTranscription: {
          mode: "SMART",
        },
        customVocabulary: vocabulary.slice(0, 100),
      },
    };

    this.ws.send(JSON.stringify(setupMsg));
  }

  public sendAudioChunk(chunk: ArrayBuffer): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const base64Data = arrayBufferToBase64(chunk);
    const audioMsg = {
      realtimeInput: {
        mediaChunks: [
          {
            mimeType: "audio/pcm;rate=16000",
            data: base64Data,
          },
        ],
      },
    };

    this.ws.send(JSON.stringify(audioMsg));
  }

  public sendAudioStreamEnd(): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const endMsg = {
      realtimeInput: {
        audioStreamEnd: true,
      },
    };

    this.ws.send(JSON.stringify(endMsg));
  }

  private handleMessage(data: unknown): void {
    let payloadStr = "";
    if (typeof data === "string") {
      payloadStr = data;
    } else if (data instanceof ArrayBuffer) {
      payloadStr = new TextDecoder().decode(data);
    } else {
      return;
    }

    try {
      const parsed = JSON.parse(payloadStr);

      const interimText =
        parsed?.serverContent?.interimInputTranscription?.text ??
        parsed?.interimInputTranscription?.text;

      if (typeof interimText === "string") {
        this.callbacks.onTranscription?.({
          type: "interim",
          text: interimText,
        });
      }

      const finalText =
        parsed?.serverContent?.inputTranscription?.text ?? parsed?.inputTranscription?.text;

      if (typeof finalText === "string") {
        this.callbacks.onTranscription?.({
          type: "final",
          text: finalText,
        });
      }
    } catch {
      // Ignore unparseable message
    }
  }

  private scheduleSessionTimeout(): void {
    this.clearSessionTimeout();
    const duration = this.options.maxDurationMs ?? MAX_SESSION_DURATION_MS;
    this.sessionTimeoutId = setTimeout(() => {
      const timeoutErr = new Error("Session maximum duration reached (10 minutes)");
      (timeoutErr as { isTimeout?: boolean }).isTimeout = true;
      this.callbacks.onError?.(timeoutErr);
      this.close();
    }, duration);
  }

  private clearSessionTimeout(): void {
    if (this.sessionTimeoutId) {
      clearTimeout(this.sessionTimeoutId);
      this.sessionTimeoutId = null;
    }
  }

  public close(): void {
    this.isExplicitlyClosed = true;
    this.clearSessionTimeout();
    if (this.ws) {
      try {
        if (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING) {
          this.ws.close();
        }
      } catch {
        // ignore
      }
      this.ws = null;
    }
  }
}
