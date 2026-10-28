// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h } from "vue";
import UserMessage from "./UserMessage.vue";
import type { ChatMessage } from "../../types";
import { i18n } from "../../i18n";
import { useUserProfile, __resetUserProfileForTest } from "../../composables/useUserProfile";
import type { UserProfile } from "../../composables/useUserProfile";

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
    app.use(i18n);
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
    app.use(i18n);
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

  describe("user profile", () => {
    const message: ChatMessage = { id: "m", role: "user", content: "hi" };

    const mountWithProfile = (p: UserProfile | null) => {
      useUserProfile().profile.value = p;
      const app = createApp({ render: () => h(UserMessage, { message }) });
      app.use(i18n);
      app.mount(root);
      return app;
    };

    beforeEach(() => {
      __resetUserProfileForTest();
    });

    it("falls back to the localized label with no profile", () => {
      const app = mountWithProfile(null);
      expect(root.querySelector('[data-testid="user-name"]')?.textContent).toContain("You");
      expect(root.querySelector('[data-testid="user-avatar"]')).toBeNull();
      expect(root.querySelector('[data-testid="user-initials"]')).toBeNull();
      app.unmount();
    });

    it("shows the name and local avatar image", () => {
      const app = mountWithProfile({
        subject: "s",
        name: "Ada Lovelace",
        avatarUrl: "/api/auth/avatar",
      });
      expect(root.querySelector('[data-testid="user-name"]')?.textContent).toContain(
        "Ada Lovelace",
      );
      expect(root.textContent).not.toContain("You");
      expect(root.querySelector('[data-testid="user-avatar"]')?.getAttribute("src")).toBe(
        "/api/auth/avatar",
      );
      app.unmount();
    });

    it("shows initials when there is no picture", () => {
      const app = mountWithProfile({ subject: "s", name: "Ada Lovelace" });
      expect(root.querySelector('[data-testid="user-avatar"]')).toBeNull();
      expect(root.querySelector('[data-testid="user-initials"]')?.textContent?.trim()).toBe("AL");
      app.unmount();
    });
  });
});
