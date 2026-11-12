// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import VoiceInputModal from "./VoiceInputModal.vue";

vi.mock("../../i18n", () => ({
  t: (key: string) => key,
}));

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("VoiceInputModal.vue", () => {
  let root: HTMLDivElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    root.remove();
  });

  it("renders with dialog role and aria-modal attributes", async () => {
    const app = createApp({
      render() {
        return h(VoiceInputModal, {
          isOpen: true,
          isRecording: true,
          isConnecting: false,
          isStopping: false,
          livePreviewText: "Testing voice",
          interimText: "",
        });
      },
    });

    app.mount(root);
    await nextTick();

    const dialog = root.querySelector("dialog");
    expect(dialog).not.toBeNull();
    expect(dialog?.hasAttribute("open")).toBe(true);
    expect(dialog?.getAttribute("aria-label")).toBeTruthy();
    expect(dialog?.hasAttribute("aria-modal")).toBe(false);
    expect(root.querySelector('[data-testid="voice-modal-pending-text"]')?.textContent).toContain(
      "Testing voice",
    );

    app.unmount();
  });

  it("emits cancel when Escape is pressed while open", async () => {
    let cancelCalled = false;
    const app = createApp({
      render() {
        return h(VoiceInputModal, {
          isOpen: true,
          isRecording: true,
          isConnecting: false,
          isStopping: false,
          livePreviewText: "",
          interimText: "",
          onCancel: () => {
            cancelCalled = true;
          },
        });
      },
    });

    app.mount(root);
    await nextTick();

    const dialog = root.querySelector("dialog") as HTMLDialogElement;
    dialog.dispatchEvent(new Event("cancel", { cancelable: true }));
    dialog.close();
    expect(cancelCalled).toBe(true);

    app.unmount();
  });

  it("does not emit cancel when Escape is pressed while closed", async () => {
    let cancelCalled = false;
    const app = createApp({
      render() {
        return h(VoiceInputModal, {
          isOpen: false,
          isRecording: false,
          isConnecting: false,
          isStopping: false,
          livePreviewText: "",
          interimText: "",
          onCancel: () => {
            cancelCalled = true;
          },
        });
      },
    });

    app.mount(root);
    await nextTick();

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(cancelCalled).toBe(false);

    app.unmount();
  });

  it("emits stop when clicking stop button", async () => {
    let stopCalled = false;
    const app = createApp({
      render() {
        return h(VoiceInputModal, {
          isOpen: true,
          isRecording: true,
          isConnecting: false,
          isStopping: false,
          livePreviewText: "",
          interimText: "",
          onStop: () => {
            stopCalled = true;
          },
        });
      },
    });

    app.mount(root);
    await nextTick();

    const stopBtn = root.querySelector('[data-testid="voice-modal-stop-btn"]') as HTMLButtonElement;
    expect(stopBtn).not.toBeNull();
    stopBtn.click();
    expect(stopCalled).toBe(true);

    app.unmount();
  });
});
