export const AUDIO_WORKLET_PROCESSOR_NAME = "pcm-encoder-worklet";

export const AUDIO_WORKLET_CODE = `
class PCMEncoderWorkletProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.buffer = [];
    this.targetSampleRate = (options && options.processorOptions && options.processorOptions.targetSampleRate) || 16000;
    this.chunkDurationMs = (options && options.processorOptions && options.processorOptions.chunkDurationMs) || 100;
    this.targetChunkSize = Math.floor((this.targetSampleRate * this.chunkDurationMs) / 1000);
    this.resampleRatio = typeof sampleRate !== "undefined" && sampleRate > 0 ? sampleRate / this.targetSampleRate : 1.0;
    this.resamplePos = 0;
    this.lastInputSample = 0;

    this.port.onmessage = (event) => {
      if (event.data === "flush") {
        this.flush();
      } else if (event.data === "clear") {
        this.buffer = [];
        this.resamplePos = 0;
      }
    };
  }

  process(inputs, outputs, parameters) {
    const input = inputs[0];
    if (!input || input.length === 0) {
      return true;
    }

    const channelData = input[0];
    if (!channelData || channelData.length === 0) {
      return true;
    }

    if (Math.abs(this.resampleRatio - 1.0) < 1e-4) {
      for (let i = 0; i < channelData.length; i++) {
        this.buffer.push(channelData[i]);
      }
    } else {
      // Software linear interpolation downsampling
      const inLen = channelData.length;
      while (this.resamplePos < inLen) {
        const idx = Math.floor(this.resamplePos);
        const frac = this.resamplePos - idx;
        const s0 = idx === 0 ? this.lastInputSample : channelData[idx - 1];
        const s1 = channelData[idx];
        const interpolated = s0 + frac * (s1 - s0);
        this.buffer.push(interpolated);
        this.resamplePos += this.resampleRatio;
      }
      this.resamplePos -= inLen;
      this.lastInputSample = channelData[inLen - 1];
    }

    while (this.buffer.length >= this.targetChunkSize) {
      const chunkSamples = this.buffer.splice(0, this.targetChunkSize);
      this.emitChunk(chunkSamples);
    }

    return true;
  }

  emitChunk(samples) {
    const int16Array = new Int16Array(samples.length);
    for (let i = 0; i < samples.length; i++) {
      let s = Math.max(-1.0, Math.min(1.0, samples[i]));
      int16Array[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
    }
    const chunkBuffer = int16Array.buffer;
    this.port.postMessage(chunkBuffer, [chunkBuffer]);
  }

  flush() {
    if (this.buffer.length > 0) {
      const chunkSamples = this.buffer;
      this.buffer = [];
      this.emitChunk(chunkSamples);
    }
    this.port.postMessage("flushed");
  }
}

registerProcessor("${AUDIO_WORKLET_PROCESSOR_NAME}", PCMEncoderWorkletProcessor);
`;

export function createAudioWorkletBlobUrl(): string {
  const blob = new Blob([AUDIO_WORKLET_CODE], { type: "application/javascript" });
  return URL.createObjectURL(blob);
}
