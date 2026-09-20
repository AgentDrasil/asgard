// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h } from "vue";
import ActivityMessage from "./ActivityMessage.vue";
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

describe("ActivityMessage.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  it("renders error message and shows retry button when canRetry is true", async () => {
    const message: ChatMessage = {
      id: "err-1",
      role: "error",
      content: "network connection refused",
      timestamp: 1725120000000,
    };

    let retried = false;
    const app = createApp({
      render() {
        return h(ActivityMessage, {
          message,
          activeAgent: null,
          canRetry: true,
          onRetry: () => {
            retried = true;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    expect(root.textContent).toContain("network connection refused");
    const retryBtn = root.querySelector("button");
    expect(retryBtn).not.toBeNull();

    retryBtn?.click();
    expect(retried).toBe(true);

    app.unmount();
  });

  it("does not render retry button when canRetry is false or omitted", () => {
    const message: ChatMessage = {
      id: "err-2",
      role: "error",
      content: "failed task",
      timestamp: 1725120000000,
    };

    const app = createApp({
      render() {
        return h(ActivityMessage, {
          message,
          activeAgent: null,
          canRetry: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);

    expect(root.textContent).toContain("failed task");
    const retryBtn = root.querySelector("button");
    expect(retryBtn).toBeNull();

    app.unmount();
  });
});
