// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick, ref } from "vue";
import ChatInput from "./ChatInput.vue";
import * as api from "../lib/api";
import { i18n } from "../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("ChatInput.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders with default props and sends text", async () => {
    let sentText = "";
    let sentAttachments: any = null;

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          modelValue: "Hello world",
          onSend: (text: string, atts?: any) => {
            sentText = text;
            sentAttachments = atts;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const sendBtn = root.querySelector('button[title^="Send message"]') as HTMLButtonElement;
    expect(sendBtn).not.toBeNull();
    expect(sendBtn.disabled).toBe(false);

    sendBtn.click();
    await nextTick();

    expect(sentText).toBe("Hello world");
    expect(sentAttachments).toBeUndefined();

    app.unmount();
  });

  it("sends message on Ctrl+Enter keydown", async () => {
    let sentText = "";
    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          modelValue: "Sent via shortcut",
          onSend: (text: string) => {
            sentText = text;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const textarea = root.querySelector("textarea") as HTMLTextAreaElement;
    expect(textarea).not.toBeNull();

    const enterEvent = new KeyboardEvent("keydown", {
      ctrlKey: true,
      key: "Enter",
      code: "Enter",
      bubbles: true,
      cancelable: true,
    });
    textarea.dispatchEvent(enterEvent);
    await nextTick();

    expect(sentText).toBe("Sent via shortcut");
    app.unmount();
  });

  it("hides attach button when no sessionId is provided", async () => {
    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: null,
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const attachBtn = root.querySelector('button[title="Attach file"]');
    expect(attachBtn).toBeNull();

    app.unmount();
  });

  it("handles file selection via input change", async () => {
    const mockAttachment = {
      name: "diagram.png",
      path: ".attachments/diagram.png",
      size: 2048,
      mimeType: "image/png",
    };
    const uploadSpy = vi.spyOn(api, "uploadAttachment").mockResolvedValue(mockAttachment);

    let sentText = "";
    let sentAttachments: any = null;

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
          modelValue: "Check this image",
          onSend: (text: string, atts?: any) => {
            sentText = text;
            sentAttachments = atts;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const attachBtn = root.querySelector('button[title="Attach file"]') as HTMLButtonElement;
    expect(attachBtn).not.toBeNull();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).not.toBeNull();

    const file = new File(["dummy"], "diagram.png", { type: "image/png" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });

    fileInput.dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(uploadSpy).toHaveBeenCalledWith("sess-123", file);
      expect(root.textContent).toContain("diagram.png");
      expect(root.textContent).toContain("(2.0 KB)");
    });

    const sendBtn = root.querySelector('button[title^="Send message"]') as HTMLButtonElement;
    sendBtn.click();
    await nextTick();

    expect(sentText).toBe("Check this image");
    expect(sentAttachments).toEqual([mockAttachment]);

    // Attachments should be cleared after send
    await nextTick();
    expect(root.textContent).not.toContain("diagram.png");

    app.unmount();
  });

  it("handles clipboard paste with files", async () => {
    const mockAttachment = {
      name: "clipboard.png",
      path: ".attachments/clipboard.png",
      size: 1024,
      mimeType: "image/png",
    };
    const uploadSpy = vi.spyOn(api, "uploadAttachment").mockResolvedValue(mockAttachment);

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const textarea = root.querySelector("textarea") as HTMLTextAreaElement;
    expect(textarea).not.toBeNull();

    const file = new File(["content"], "clipboard.png", { type: "image/png" });
    const pasteEvent = new Event("paste", { bubbles: true, cancelable: true }) as any;
    pasteEvent.clipboardData = {
      files: [file],
    };

    textarea.dispatchEvent(pasteEvent);

    await vi.waitFor(() => {
      expect(uploadSpy).toHaveBeenCalledWith("sess-123", file);
      expect(root.textContent).toContain("clipboard.png");
    });

    app.unmount();
  });

  it("handles drag and drop files", async () => {
    const mockAttachment = {
      name: "drop.txt",
      path: ".attachments/drop.txt",
      size: 512,
      mimeType: "text/plain",
    };
    const uploadSpy = vi.spyOn(api, "uploadAttachment").mockResolvedValue(mockAttachment);

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const container = root.firstElementChild as HTMLElement;
    expect(container).not.toBeNull();

    // Drag over
    container.dispatchEvent(new Event("dragover", { bubbles: true, cancelable: true }));
    await nextTick();

    // Drop
    const file = new File(["text"], "drop.txt", { type: "text/plain" });
    const dropEvent = new Event("drop", { bubbles: true, cancelable: true }) as any;
    dropEvent.dataTransfer = {
      files: [file],
    };

    container.dispatchEvent(dropEvent);

    await vi.waitFor(() => {
      expect(uploadSpy).toHaveBeenCalledWith("sess-123", file);
      expect(root.textContent).toContain("drop.txt");
    });

    app.unmount();
  });

  it("removes attachment when clicking remove button", async () => {
    const mockAttachment = {
      name: "remove-me.pdf",
      path: ".attachments/remove-me.pdf",
      size: 4096,
      mimeType: "application/pdf",
    };
    vi.spyOn(api, "uploadAttachment").mockResolvedValue(mockAttachment);

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["pdf"], "remove-me.pdf", { type: "application/pdf" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });

    fileInput.dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(root.textContent).toContain("remove-me.pdf");
    });

    const removeBtn = root.querySelector('button[title="Remove attachment"]') as HTMLButtonElement;
    expect(removeBtn).not.toBeNull();
    removeBtn.click();

    await nextTick();
    expect(root.textContent).not.toContain("remove-me.pdf");

    app.unmount();
  });

  it("does not enable send for attachment-only message with empty text", async () => {
    const mockAttachment = {
      name: "a.png",
      path: ".attachments/a.png",
      size: 1024,
      mimeType: "image/png",
    };
    vi.spyOn(api, "uploadAttachment").mockResolvedValue(mockAttachment);

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
          modelValue: "",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["dummy"], "a.png", { type: "image/png" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });

    fileInput.dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(root.textContent).toContain("a.png");
    });

    const sendBtn = root.querySelector('button[title^="Send message"]') as HTMLButtonElement;
    expect(sendBtn.disabled).toBe(true);

    app.unmount();
  });

  it("rejects single files exceeding 20MB without calling uploadAttachment", async () => {
    const uploadSpy = vi.spyOn(api, "uploadAttachment");

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const largeFile = new File(["x".repeat(100)], "huge.zip", { type: "application/zip" });
    Object.defineProperty(largeFile, "size", { value: 25 * 1024 * 1024 });

    Object.defineProperty(fileInput, "files", {
      value: [largeFile],
      writable: true,
    });

    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(uploadSpy).not.toHaveBeenCalled();
    expect(root.textContent).not.toContain("huge.zip");

    app.unmount();
  });

  it("rejects files when total count exceeds 20", async () => {
    const uploadSpy = vi.spyOn(api, "uploadAttachment");

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const files = Array.from(
      { length: 21 },
      (_, i) => new File(["test"], `file-${i}.txt`, { type: "text/plain" }),
    );

    Object.defineProperty(fileInput, "files", {
      value: files,
      writable: true,
    });

    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(uploadSpy).not.toHaveBeenCalled();

    app.unmount();
  });

  it("deduplicates files before uploading", async () => {
    const mockAttachment = {
      name: "duplicate.txt",
      path: ".attachments/duplicate.txt",
      size: 100,
      mimeType: "text/plain",
    };
    const uploadSpy = vi.spyOn(api, "uploadAttachment").mockResolvedValue(mockAttachment);

    const app = createApp({
      render() {
        return h(ChatInput, {
          loading: false,
          sessionId: "sess-123",
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file1 = new File(["content-a"], "duplicate.txt", { type: "text/plain" });
    Object.defineProperty(file1, "size", { value: 100 });

    Object.defineProperty(fileInput, "files", {
      value: [file1],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(uploadSpy).toHaveBeenCalledTimes(1);
    });

    // Try uploading the exact same file again
    const file2 = new File(["content-b"], "duplicate.txt", { type: "text/plain" });
    Object.defineProperty(file2, "size", { value: 100 });

    Object.defineProperty(fileInput, "files", {
      value: [file2],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    // uploadSpy should still only have been called once
    expect(uploadSpy).toHaveBeenCalledTimes(1);

    app.unmount();
  });

  it("rejects files exceeding 50MB total size limit", async () => {
    const toastModule = await import("../composables/useToast");
    const mockToastError = vi.fn<(...args: any[]) => void>();
    vi.spyOn(toastModule, "useToast").mockReturnValue({
      error: mockToastError,
      success: vi.fn<(...args: any[]) => void>(),
      warning: vi.fn<(...args: any[]) => void>(),
      info: vi.fn<(...args: any[]) => void>(),
    } as any);

    vi.spyOn(api, "uploadAttachment").mockImplementation(async (_sessId, file) => ({
      name: file.name,
      path: `.attachments/${file.name}`,
      size: file.size,
      mimeType: file.type,
    }));

    const root = document.createElement("div");
    document.body.appendChild(root);

    const app = createApp({
      render() {
        return h(ChatInput, {
          sessionId: "test-session-123",
          loading: false,
          modelValue: "",
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file1 = new File(["payload1"], "large1.bin", { type: "application/octet-stream" });
    Object.defineProperty(file1, "size", { value: 18 * 1024 * 1024 });
    const file2 = new File(["payload2"], "large2.bin", { type: "application/octet-stream" });
    Object.defineProperty(file2, "size", { value: 18 * 1024 * 1024 });
    const file3 = new File(["payload3"], "large3.bin", { type: "application/octet-stream" });
    Object.defineProperty(file3, "size", { value: 18 * 1024 * 1024 });

    Object.defineProperty(fileInput, "files", {
      value: [file1, file2, file3],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(mockToastError).toHaveBeenCalledWith("Total attachment size exceeds 50MB limit");
    });

    app.unmount();
  });

  describe("Voice Input Integration", () => {
    let mockVoiceInputState: any;
    let registeredCallbacks: any;

    beforeEach(async () => {
      const voiceModule = await import("../composables/useVoiceInput");
      mockVoiceInputState = {
        isRecording: ref(false),
        isConnecting: ref(false),
        isStopping: ref(false),
        interimText: ref(""),
        livePreviewText: ref(""),
        error: ref(null),
        startRecording: vi.fn<() => Promise<void>>(),
        stopRecording: vi.fn<() => Promise<void>>(),
        cancelRecording: vi.fn<() => void>(),
      };

      vi.spyOn(voiceModule, "useVoiceInput").mockImplementation((callbacks?: any) => {
        registeredCallbacks = callbacks;
        return {
          isRecording: mockVoiceInputState.isRecording,
          isConnecting: mockVoiceInputState.isConnecting,
          isStopping: mockVoiceInputState.isStopping,
          interimText: mockVoiceInputState.interimText,
          livePreviewText: mockVoiceInputState.livePreviewText,
          error: mockVoiceInputState.error,
          state: ref("idle"),
          startRecording: mockVoiceInputState.startRecording,
          stopRecording: mockVoiceInputState.stopRecording,
          cancelRecording: mockVoiceInputState.cancelRecording,
        } as any;
      });
    });

    it("should render VoiceInputButton in both standard input and expanded modal", async () => {
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: "",
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      // Standard input voice button (first one)
      const voiceBtns = root.querySelectorAll('button[title^="Start voice recording"]');
      expect(voiceBtns.length).toBe(2); // One in standard bar, one in dialog

      app.unmount();
    });

    it("should toggle voice recording when button is clicked", async () => {
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: "",
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      const voiceBtn = root.querySelectorAll(
        'button[title^="Start voice recording"]',
      )[0] as HTMLButtonElement;
      expect(voiceBtn).not.toBeNull();

      // Click when idle -> startRecording
      voiceBtn.click();
      expect(mockVoiceInputState.startRecording).toHaveBeenCalled();

      // Set recording to true -> title becomes "Stop recording"
      mockVoiceInputState.isRecording.value = true;
      await nextTick();

      const stopBtn = root.querySelector('button[title^="Stop recording"]') as HTMLButtonElement;
      expect(stopBtn).not.toBeNull();

      stopBtn.click();
      expect(mockVoiceInputState.stopRecording).toHaveBeenCalled();

      app.unmount();
    });

    it("should render interim text preview banner when recording with interimText or livePreviewText", async () => {
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: "",
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      expect(root.querySelector('[data-testid="voice-preview-banner"]')).toBeNull();

      mockVoiceInputState.isRecording.value = true;
      mockVoiceInputState.livePreviewText.value = "Hello from speech";
      await nextTick();

      const banner = root.querySelector('[data-testid="voice-preview-banner"]');
      expect(banner).not.toBeNull();
      expect(banner?.textContent).toContain("Hello from speech");
      expect(banner?.textContent).toContain("Recording...");

      app.unmount();
    });

    it("should append finalized speech transcription to existing text draft with space", async () => {
      let currentVal = "Existing draft";
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: currentVal,
            "onUpdate:modelValue": (v: string) => {
              currentVal = v;
            },
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      expect(registeredCallbacks?.onFinalText).toBeDefined();
      registeredCallbacks.onFinalText("additional voice note");
      await nextTick();

      expect(currentVal).toBe("Existing draft additional voice note");

      app.unmount();
    });

    it("should not modify draft if finalized speech transcription is empty string", async () => {
      let currentVal = "Unchanged text";
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: currentVal,
            "onUpdate:modelValue": (v: string) => {
              currentVal = v;
            },
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      expect(registeredCallbacks?.onFinalText).toBeDefined();
      registeredCallbacks.onFinalText("   ");
      await nextTick();

      expect(currentVal).toBe("Unchanged text");

      app.unmount();
    });

    it("should disable voice button when isStopping is true", async () => {
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: "",
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      mockVoiceInputState.isStopping.value = true;
      await nextTick();

      const stoppingBtn = root.querySelector(
        'button[title^="Processing transcription..."]',
      ) as HTMLButtonElement;
      expect(stoppingBtn).not.toBeNull();
      expect(stoppingBtn.disabled).toBe(true);

      stoppingBtn.click();
      expect(mockVoiceInputState.stopRecording).not.toHaveBeenCalled();
      expect(mockVoiceInputState.startRecording).not.toHaveBeenCalled();

      app.unmount();
    });

    it("should trigger cancelRecording on component unmount and modal close", async () => {
      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: "",
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      // Open modal
      const expandBtn = root.querySelector(
        'button[title^="Expand input editor"]',
      ) as HTMLButtonElement;
      expandBtn.click();
      await nextTick();

      // While recording, closing modal should call cancelRecording
      mockVoiceInputState.isRecording.value = true;
      const closeModalBtn = root.querySelector('button[title^="Close modal"]') as HTMLButtonElement;
      closeModalBtn.click();
      await nextTick();

      expect(mockVoiceInputState.cancelRecording).toHaveBeenCalled();

      // Unmounting component should also call cancelRecording
      app.unmount();
      expect(mockVoiceInputState.cancelRecording).toHaveBeenCalledTimes(2);
    });

    it("should trigger localized toast notification using VOICE_ERROR_I18N_MAP when voiceError is set", async () => {
      const toastModule = await import("../composables/useToast");
      const mockToastError = vi.fn<(...args: any[]) => void>();
      vi.spyOn(toastModule, "useToast").mockReturnValue({
        error: mockToastError,
        success: vi.fn<(...args: any[]) => void>(),
        warning: vi.fn<(...args: any[]) => void>(),
        info: vi.fn<(...args: any[]) => void>(),
      } as any);

      const app = createApp({
        render() {
          return h(ChatInput, {
            loading: false,
            modelValue: "",
          });
        },
      });
      app.use(i18n);
      app.mount(root);

      mockVoiceInputState.error.value = "micDenied";
      await nextTick();

      expect(mockToastError).toHaveBeenCalledWith(
        "Microphone access denied. Please grant permission in browser settings.",
      );

      app.unmount();
    });
  });
});
