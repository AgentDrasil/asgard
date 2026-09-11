// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import AskUserCard from "./AskUserCard.vue";
import type { ChatMessage } from "../../types";
import { i18n } from "../../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

const { sendAskUserReply } = vi.hoisted(() => ({
  sendAskUserReply:
    vi.fn<(sessionId: string, messageId: string, text: string) => Promise<boolean>>(),
}));

vi.mock("../../lib/api", () => ({
  sendAskUserReply,
}));

const flushPromises = () => new Promise((resolve) => setTimeout(resolve, 0));

const askingMessage: ChatMessage = {
  id: "ask-1",
  role: "ask_user",
  agentName: "Reviewer",
  content: "How should I proceed?",
  timestamp: 1725120000000,
};

describe("AskUserCard.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.clearAllMocks();
    sendAskUserReply.mockResolvedValue(true);
  });

  const mountCard = async () => {
    const app = createApp({
      render() {
        return h(AskUserCard, {
          message: askingMessage,
          sessionId: "chat-123",
          activeAgent: null,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();
    return app;
  };

  it("requires the send shortcut instead of plain Enter to submit a reply", async () => {
    const app = await mountCard();

    const input = root.querySelector<HTMLInputElement>('input[type="text"]');
    expect(input).not.toBeNull();
    input!.value = "go ahead";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    // Plain Enter must not submit the reply
    input!.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
    await flushPromises();
    expect(sendAskUserReply).not.toHaveBeenCalled();

    // Ctrl+Enter submits
    input!.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    await flushPromises();
    expect(sendAskUserReply).toHaveBeenCalledWith("chat-123", "ask-1", "go ahead");

    app.unmount();
  });
});
