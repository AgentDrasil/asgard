// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import ChatInput from "./ChatInput.vue";
import * as api from "../lib/api";

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
});
