import { AUDIO_WORKLET_PROCESSOR_NAME, createAudioWorkletBlobUrl } from "./audioWorkletProcessor";

export interface AudioRecorderOptions {
  chunkDurationMs?: number;
  targetSampleRate?: number;
}

export interface AudioRecorderCallbacks {
  onData: (chunk: ArrayBuffer) => void;
  onError?: (error: Error) => void;
  onStateChange?: (state: "idle" | "recording" | "stopped") => void;
}

export class AudioRecorder {
  private options: Required<AudioRecorderOptions>;
  private callbacks: AudioRecorderCallbacks;
  private state: "idle" | "recording" | "stopped" = "idle";

  private mediaStream: MediaStream | null = null;
  private audioContext: AudioContext | null = null;
  private sourceNode: MediaStreamAudioSourceNode | null = null;
  private workletNode: AudioWorkletNode | null = null;
  private blobUrl: string | null = null;

  constructor(callbacks: AudioRecorderCallbacks, options?: AudioRecorderOptions) {
    this.callbacks = callbacks;
    this.options = {
      chunkDurationMs: options?.chunkDurationMs ?? 100,
      targetSampleRate: options?.targetSampleRate ?? 16000,
    };
  }

  public getState(): "idle" | "recording" | "stopped" {
    return this.state;
  }

  public async start(): Promise<void> {
    if (this.state === "recording") {
      return;
    }

    try {
      const globalScope = typeof window !== "undefined" ? window : globalThis;
      const mediaDevices = typeof navigator !== "undefined" ? navigator.mediaDevices : undefined;

      if (!mediaDevices?.getUserMedia) {
        throw new Error("getUserMedia is not supported in this environment");
      }

      this.mediaStream = await mediaDevices.getUserMedia({
        audio: {
          channelCount: 1,
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        },
      });

      const AudioContextClass =
        (globalScope as unknown as { AudioContext?: typeof AudioContext }).AudioContext ||
        (globalScope as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;

      if (!AudioContextClass) {
        throw new Error("AudioContext is not supported in this environment");
      }

      try {
        this.audioContext = new AudioContextClass({
          sampleRate: this.options.targetSampleRate,
        });
      } catch (err) {
        console.warn(
          "Could not create AudioContext with target sampleRate, falling back to default AudioContext",
          err,
        );
        this.audioContext = new AudioContextClass();
      }

      if (this.audioContext.sampleRate !== this.options.targetSampleRate) {
        console.info(
          `AudioContext initialized with sampleRate ${this.audioContext.sampleRate}Hz; software resampling to ${this.options.targetSampleRate}Hz will be applied in worklet`,
        );
      }

      this.blobUrl = createAudioWorkletBlobUrl();
      await this.audioContext.audioWorklet.addModule(this.blobUrl);

      this.workletNode = new AudioWorkletNode(this.audioContext, AUDIO_WORKLET_PROCESSOR_NAME, {
        processorOptions: {
          targetSampleRate: this.options.targetSampleRate,
          chunkDurationMs: this.options.chunkDurationMs,
        },
      });

      this.workletNode.port.onmessage = (event: MessageEvent<ArrayBuffer | string>) => {
        if (event.data instanceof ArrayBuffer) {
          if (this.state === "recording") {
            this.callbacks.onData(event.data);
          }
        }
      };

      this.sourceNode = this.audioContext.createMediaStreamSource(this.mediaStream);
      this.sourceNode.connect(this.workletNode);

      this.setState("recording");
    } catch (error: unknown) {
      this.cleanup();
      const err = error instanceof Error ? error : new Error(String(error));
      this.callbacks.onError?.(err);
      throw err;
    }
  }

  public async stop(): Promise<void> {
    if (this.state === "stopped" || this.state === "idle") {
      return;
    }

    if (this.workletNode) {
      const node = this.workletNode;
      await new Promise<void>((resolve) => {
        const timeout = setTimeout(resolve, 100);
        const originalOnMessage = node.port.onmessage;

        node.port.onmessage = (event: MessageEvent<ArrayBuffer | string>) => {
          if (event.data === "flushed") {
            clearTimeout(timeout);
            resolve();
          } else if (event.data instanceof ArrayBuffer) {
            this.callbacks.onData(event.data);
          } else if (originalOnMessage) {
            originalOnMessage.call(node.port, event);
          }
        };

        try {
          node.port.postMessage("flush");
        } catch {
          clearTimeout(timeout);
          resolve();
        }
      });
    }

    this.cleanup();
    this.setState("stopped");
  }

  private cleanup(): void {
    if (this.mediaStream) {
      for (const track of this.mediaStream.getTracks()) {
        try {
          track.stop();
        } catch {
          // ignore track stop errors
        }
      }
      this.mediaStream = null;
    }

    if (this.sourceNode) {
      try {
        this.sourceNode.disconnect();
      } catch {
        // ignore disconnect errors
      }
      this.sourceNode = null;
    }

    if (this.workletNode) {
      try {
        this.workletNode.disconnect();
        this.workletNode.port.onmessage = null;
      } catch {
        // ignore disconnect errors
      }
      this.workletNode = null;
    }

    if (this.audioContext) {
      try {
        if (this.audioContext.state !== "closed") {
          this.audioContext.close().catch(() => {
            // ignore async close rejection
          });
        }
      } catch {
        // ignore close errors
      }
      this.audioContext = null;
    }

    if (this.blobUrl) {
      try {
        URL.revokeObjectURL(this.blobUrl);
      } catch {
        // ignore revoke errors
      }
      this.blobUrl = null;
    }
  }

  private setState(state: "idle" | "recording" | "stopped"): void {
    this.state = state;
    this.callbacks.onStateChange?.(state);
  }
}
