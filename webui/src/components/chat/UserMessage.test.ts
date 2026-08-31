// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h } from "vue";
import UserMessage from "./UserMessage.vue";
import type { ChatMessage } from "../../types";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("UserMessage.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders user message without attachments", () => {
    const message: ChatMessage = {
      id: "msg-1",
      role: "user",
      content: "Hello from test",
      timestamp: 1725120000000,
    };

    const app = createApp({
      render() {
        return h(UserMessage, {
          message,
          sessionId: "sess-123",
        });
      },
    });
    app.mount(root);

    expect(root.textContent).toContain("Hello from test");
    expect(root.textContent).toContain("You");
    expect(root.querySelector("a")).toBeNull();

    app.unmount();
  });

  it("renders user message with attachments, file size and download link", () => {
    const message: ChatMessage = {
      id: "msg-2",
      role: "user",
      content: "Here is the architectural diagram and code",
      timestamp: 1725120000000,
      attachments: [
        {
          name: "arch.png",
          path: ".attachments/arch.png",
          size: 1048576,
          mimeType: "image/png",
        },
        {
          name: "data.csv",
          path: ".attachments/data.csv",
          size: 2048,
          mimeType: "text/csv",
        },
      ],
    };

    const app = createApp({
      render() {
        return h(UserMessage, {
          message,
          sessionId: "sess-123",
        });
      },
    });
    app.mount(root);

    expect(root.textContent).toContain("Here is the architectural diagram and code");
    expect(root.textContent).toContain("arch.png");
    expect(root.textContent).toContain("1.0 MB");
    expect(root.textContent).toContain("data.csv");
    expect(root.textContent).toContain("2.0 KB");

    const links = root.querySelectorAll("a");
    expect(links.length).toBe(2);

    expect(links[0].getAttribute("href")).toBe("/api/sessions/sess-123/attachments/arch.png");
    expect(links[1].getAttribute("href")).toBe("/api/sessions/sess-123/attachments/data.csv");

    app.unmount();
  });
});
