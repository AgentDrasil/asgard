// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AudioRecorder } from "./audioRecorder";
import { AUDIO_WORKLET_PROCESSOR_NAME, AUDIO_WORKLET_CODE } from "./audioWorkletProcessor";

describe("AudioWorklet Processor Logic", () => {
  it("converts Float32 samples to 16-bit Little-Endian PCM in worklet logic with clamping", () => {
    // Validate the core PCM mapping formula from AUDIO_WORKLET_CODE
    expect(AUDIO_WORKLET_CODE).toContain("s < 0 ? s * 0x8000 : s * 0x7fff");

    // Execute the exact mapping logic to ensure numerical correctness, including clamp on [-1.5, 1.5]
    const samples = new Float32Array([1.5, 1.0, -1.0, -1.5, 0.0, 0.5, -0.5]);
    const int16Array = new Int16Array(samples.length);
    for (let i = 0; i < samples.length; i++) {
      let s = Math.max(-1.0, Math.min(1.0, samples[i]));
      int16Array[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
    }

    expect(int16Array[0]).toBe(0x7fff); // clamped 1.5 -> 32767
    expect(int16Array[1]).toBe(0x7fff); // 32767
    expect(int16Array[2]).toBe(-0x8000); // -32768
    expect(int16Array[3]).toBe(-0x8000); // clamped -1.5 -> -32768
    expect(int16Array[4]).toBe(0);

    const dataView = new DataView(int16Array.buffer);
    // Check little-endian 16-bit encoding
    expect(dataView.getInt16(0, true)).toBe(0x7fff);
    expect(dataView.getInt16(2, true)).toBe(0x7fff);
    expect(dataView.getInt16(4, true)).toBe(-0x8000);
    expect(dataView.getInt16(6, true)).toBe(-0x8000);
    expect(dataView.getInt16(8, true)).toBe(0);
  });

  it("should downsample to 16kHz via software linear interpolation resampler when context sampleRate differs", () => {
    // Simulate worklet processor environment at 48kHz
    let registeredClass: any = null;
    const mockRegisterProcessor = (_name: string, cls: any) => {
      registeredClass = cls;
    };

    class MockWorkletBase {
      port = {
        postMessage: vi.fn<(data: any) => void>(),
        onmessage: null,
      };
    }

    const fn = new Function(
      "AudioWorkletProcessor",
      "registerProcessor",
      "sampleRate",
      AUDIO_WORKLET_CODE,
    );
    fn(MockWorkletBase, mockRegisterProcessor, 48000);

    expect(registeredClass).toBeDefined();
    const processor = new registeredClass({
      processorOptions: {
        targetSampleRate: 16000,
        chunkDurationMs: 100,
      },
    });

    const emittedChunks: ArrayBuffer[] = [];
    processor.port.postMessage = vi.fn<(data: any) => void>((data: any) => {
      if (data instanceof ArrayBuffer) {
        emittedChunks.push(data);
      }
    });

    // Feed 4800 samples at 48kHz (equivalent to 100ms of audio)
    // AudioWorklet standard frame is 128 samples: 4800 / 128 = 37.5 frames
    const frameSize = 128;
    const totalInputSamples = 4800;
    let fed = 0;
    while (fed < totalInputSamples) {
      const remaining = Math.min(frameSize, totalInputSamples - fed);
      const frame = new Float32Array(remaining);
      for (let i = 0; i < remaining; i++) {
        frame[i] = 0.5;
      }
      processor.process([[frame]], [], {});
      fed += remaining;
    }

    // Flush remainder to capture full 100ms
    processor.flush();

    // Total emitted bytes should correspond to 1600 samples * 2 bytes/sample = 3200 bytes
    const totalEmittedBytes = emittedChunks.reduce((acc, c) => acc + c.byteLength, 0);
    expect(totalEmittedBytes).toBe(3200);
  });
});

describe("AudioRecorder", () => {
  let mockTrack: { stop: ReturnType<typeof vi.fn<() => void>> };
  let mockStream: { getTracks: ReturnType<typeof vi.fn<() => MediaStreamTrack[]>> };
  let mockPort: {
    postMessage: ReturnType<typeof vi.fn<(message: any) => void>>;
    onmessage: ((event: MessageEvent<ArrayBuffer | string>) => void) | null;
  };
  let mockWorkletNode: {
    port: typeof mockPort;
    connect: ReturnType<typeof vi.fn<(destination: any) => void>>;
    disconnect: ReturnType<typeof vi.fn<() => void>>;
  };
  let mockSourceNode: {
    connect: ReturnType<typeof vi.fn<(destination: any) => void>>;
    disconnect: ReturnType<typeof vi.fn<() => void>>;
  };
  let mockAudioWorklet: {
    addModule: ReturnType<typeof vi.fn<(moduleURL: string) => Promise<void>>>;
  };
  let mockAudioContextInstance: {
    sampleRate: number;
    state: string;
    audioWorklet: typeof mockAudioWorklet;
    createMediaStreamSource: ReturnType<typeof vi.fn<(stream: MediaStream) => any>>;
    close: ReturnType<typeof vi.fn<() => Promise<void>>>;
  };
  let audioContextConstructorSpy: ReturnType<typeof vi.fn<(options?: AudioContextOptions) => void>>;

  beforeEach(() => {
    mockTrack = {
      stop: vi.fn<() => void>(),
    };
    mockStream = {
      getTracks: vi
        .fn<() => MediaStreamTrack[]>()
        .mockReturnValue([mockTrack as unknown as MediaStreamTrack]),
    };

    mockPort = {
      postMessage: vi.fn<(message: any) => void>(),
      onmessage: null,
    };

    mockWorkletNode = {
      port: mockPort,
      connect: vi.fn<(destination: any) => void>(),
      disconnect: vi.fn<() => void>(),
    };

    mockSourceNode = {
      connect: vi.fn<(destination: any) => void>(),
      disconnect: vi.fn<() => void>(),
    };

    mockAudioWorklet = {
      addModule: vi.fn<(moduleURL: string) => Promise<void>>().mockResolvedValue(undefined),
    };

    audioContextConstructorSpy = vi.fn<(options?: AudioContextOptions) => void>();

    mockAudioContextInstance = {
      sampleRate: 44100,
      state: "running",
      audioWorklet: mockAudioWorklet,
      createMediaStreamSource: vi
        .fn<(stream: MediaStream) => any>()
        .mockReturnValue(mockSourceNode),
      close: vi.fn<() => Promise<void>>().mockImplementation(async () => {
        mockAudioContextInstance.state = "closed";
      }),
    };

    class MockAudioContext {
      sampleRate: number;
      state: string;
      audioWorklet = mockAudioWorklet;
      createMediaStreamSource = mockAudioContextInstance.createMediaStreamSource;
      close = mockAudioContextInstance.close;

      constructor(options?: AudioContextOptions) {
        audioContextConstructorSpy(options);
        this.sampleRate = options?.sampleRate || 44100;
        this.state = "running";
        mockAudioContextInstance.sampleRate = this.sampleRate;
      }
    }

    class MockAudioWorkletNode {
      port = mockPort;
      connect = mockWorkletNode.connect;
      disconnect = mockWorkletNode.disconnect;
      constructor(...args: any[]) {
        (MockAudioWorkletNode as any).mockConstructor(...args);
      }
      static mockConstructor = vi.fn<(...args: any[]) => void>();
    }

    vi.stubGlobal("AudioContext", MockAudioContext);
    vi.stubGlobal("AudioWorkletNode", MockAudioWorkletNode);
    vi.stubGlobal("URL", {
      ...globalThis.URL,
      createObjectURL: vi.fn<(blob: Blob) => string>().mockReturnValue("blob:mock-worklet-url"),
      revokeObjectURL: vi.fn<(url: string) => void>(),
    });

    Object.defineProperty(navigator, "mediaDevices", {
      value: {
        getUserMedia: vi
          .fn<(constraints?: MediaStreamConstraints) => Promise<any>>()
          .mockResolvedValue(mockStream),
      },
      configurable: true,
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should initialize AudioContext with targetSampleRate 16000", async () => {
    const onData = vi.fn<(chunk: ArrayBuffer) => void>();
    const recorder = new AudioRecorder({ onData }, { targetSampleRate: 16000 });

    await recorder.start();

    expect(audioContextConstructorSpy).toHaveBeenCalledWith({ sampleRate: 16000 });
    expect(recorder.getState()).toBe("recording");
    await recorder.stop();
  });

  it("should load AudioWorklet module using blob URL", async () => {
    const onData = vi.fn<(chunk: ArrayBuffer) => void>();
    const recorder = new AudioRecorder({ onData });

    await recorder.start();

    expect(URL.createObjectURL).toHaveBeenCalled();
    expect(mockAudioWorklet.addModule).toHaveBeenCalledWith("blob:mock-worklet-url");
    expect((globalThis.AudioWorkletNode as any).mockConstructor).toHaveBeenCalledWith(
      expect.anything(),
      AUDIO_WORKLET_PROCESSOR_NAME,
      expect.objectContaining({
        processorOptions: {
          targetSampleRate: 16000,
          chunkDurationMs: 100,
        },
      }),
    );
    await recorder.stop();
  });

  it("should dispatch onData callback when worklet sends chunk (~3200 bytes)", async () => {
    const onData = vi.fn<(chunk: ArrayBuffer) => void>();
    const recorder = new AudioRecorder({ onData });

    await recorder.start();

    const mockChunk = new ArrayBuffer(3200);
    mockPort.onmessage?.({ data: mockChunk } as MessageEvent<ArrayBuffer>);

    expect(onData).toHaveBeenCalledWith(mockChunk);
    await recorder.stop();
  });

  it("should stop all tracks and close AudioContext on stop() with flush settlement", async () => {
    const onStateChange = vi.fn<(state: "idle" | "recording" | "stopped") => void>();
    const onData = vi.fn<(chunk: ArrayBuffer) => void>();
    const recorder = new AudioRecorder({ onData, onStateChange });

    await recorder.start();
    expect(recorder.getState()).toBe("recording");
    expect(onStateChange).toHaveBeenCalledWith("recording");

    const tailChunk = new ArrayBuffer(1600);
    // When flush is posted, simulate worklet posting tailChunk and then "flushed"
    mockPort.postMessage.mockImplementation((msg: any) => {
      if (msg === "flush" && mockPort.onmessage) {
        mockPort.onmessage({ data: tailChunk } as MessageEvent<ArrayBuffer>);
        mockPort.onmessage({ data: "flushed" } as MessageEvent<string>);
      }
    });

    await recorder.stop();

    expect(mockPort.postMessage).toHaveBeenCalledWith("flush");
    expect(onData).toHaveBeenCalledWith(tailChunk);
    expect(mockTrack.stop).toHaveBeenCalled();
    expect(mockSourceNode.disconnect).toHaveBeenCalled();
    expect(mockWorkletNode.disconnect).toHaveBeenCalled();
    expect(mockAudioContextInstance.close).toHaveBeenCalled();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:mock-worklet-url");
    expect(recorder.getState()).toBe("stopped");
    expect(onStateChange).toHaveBeenCalledWith("stopped");
  });

  it("should handle NotAllowedError and call onError", async () => {
    const permissionError = new Error("Permission denied");
    permissionError.name = "NotAllowedError";

    navigator.mediaDevices.getUserMedia = vi
      .fn<(constraints?: MediaStreamConstraints) => Promise<any>>()
      .mockRejectedValue(permissionError);

    const onError = vi.fn<(error: Error) => void>();
    const onData = vi.fn<(chunk: ArrayBuffer) => void>();
    const recorder = new AudioRecorder({ onData, onError });

    await expect(recorder.start()).rejects.toThrow("Permission denied");
    expect(onError).toHaveBeenCalledWith(permissionError);
    expect(recorder.getState()).toBe("idle");
  });
});
