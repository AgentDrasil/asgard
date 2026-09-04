// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import QueuedMessageCard from "./QueuedMessageCard.vue";
import type { QueuedMessage } from "../../types";
import { i18n } from "../../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("QueuedMessageCard.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
  });

  const dummyMessage: QueuedMessage = {
    id: "qmsg-01918a12-7000-7000-8000-000000000001",
    chatId: "chat-123",
    prompt: "Please refactor the auth service",
    model: "gemini-pro",
    createdAt: "2026-09-03T20:00:00Z",
    updatedAt: "2026-09-03T20:00:00Z",
  };

  it("renders with queue badge index and prompt content", async () => {
    const app = createApp({
      render() {
        return h(QueuedMessageCard, {
          message: dummyMessage,
          index: 0,
          total: 1,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const badge = root.querySelector('[data-testid="queued-badge"]');
    expect(badge).not.toBeNull();
    expect(badge?.textContent).toContain("1");

    const content = root.querySelector('[data-testid="queued-prompt-content"]');
    expect(content).not.toBeNull();
    expect(content?.textContent).toContain("Please refactor the auth service");

    app.unmount();
  });

  it("enters inline edit mode, saves edited text, and emits edit event", async () => {
    let editedId = "";
    let editedText = "";

    const app = createApp({
      render() {
        return h(QueuedMessageCard, {
          message: dummyMessage,
          index: 1,
          total: 2,
          onEdit: (id: string, text: string) => {
            editedId = id;
            editedText = text;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const editBtn = root.querySelector('[data-testid="edit-queued-button"]') as HTMLButtonElement;
    expect(editBtn).not.toBeNull();
    editBtn.click();
    await nextTick();

    const textarea = root.querySelector(
      '[data-testid="queued-edit-textarea"]',
    ) as HTMLTextAreaElement;
    expect(textarea).not.toBeNull();
    expect(textarea.value).toBe("Please refactor the auth service");

    textarea.value = "Updated prompt for auth service";
    textarea.dispatchEvent(new Event("input"));
    await nextTick();

    const saveBtn = root.querySelector('[data-testid="save-edit-button"]') as HTMLButtonElement;
    expect(saveBtn).not.toBeNull();
    saveBtn.click();
    await nextTick();

    expect(editedId).toBe(dummyMessage.id);
    expect(editedText).toBe("Updated prompt for auth service");

    // After saving, edit mode exits
    expect(root.querySelector('[data-testid="queued-edit-textarea"]')).toBeNull();

    app.unmount();
  });

  it("cancels inline edit without emitting edit event", async () => {
    let editedCalled = false;

    const app = createApp({
      render() {
        return h(QueuedMessageCard, {
          message: dummyMessage,
          index: 0,
          onEdit: () => {
            editedCalled = true;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const editBtn = root.querySelector('[data-testid="edit-queued-button"]') as HTMLButtonElement;
    editBtn.click();
    await nextTick();

    const cancelBtn = root.querySelector('[data-testid="cancel-edit-button"]') as HTMLButtonElement;
    expect(cancelBtn).not.toBeNull();
    cancelBtn.click();
    await nextTick();

    expect(editedCalled).toBe(false);
    expect(root.querySelector('[data-testid="queued-edit-textarea"]')).toBeNull();
    expect(root.querySelector('[data-testid="queued-prompt-content"]')?.textContent).toContain(
      "Please refactor the auth service",
    );

    app.unmount();
  });

  it("emits delete event when delete button is clicked", async () => {
    let deletedId = "";

    const app = createApp({
      render() {
        return h(QueuedMessageCard, {
          message: dummyMessage,
          index: 0,
          onDelete: (id: string) => {
            deletedId = id;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const deleteBtn = root.querySelector(
      '[data-testid="delete-queued-button"]',
    ) as HTMLButtonElement;
    expect(deleteBtn).not.toBeNull();
    deleteBtn.click();
    await nextTick();

    expect(deletedId).toBe(dummyMessage.id);

    app.unmount();
  });
});
