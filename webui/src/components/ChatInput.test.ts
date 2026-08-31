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
});
