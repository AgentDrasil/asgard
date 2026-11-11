// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import SessionList from "./SessionList.vue";
import type { ChatSession } from "../../types";
import { i18n } from "../../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

const sessions: ChatSession[] = [
  { chatID: "p", title: "Parent chat", currentAgent: "lead", runDir: "/ws" },
  {
    chatID: "c1",
    title: "",
    currentAgent: "reviewer",
    runDir: "/ws",
    parentChatId: "p",
    spawnLabel: "review",
    spawnReadonly: true,
    subagentState: "running",
  },
  {
    chatID: "c2",
    title: "",
    currentAgent: "coder",
    runDir: "/ws",
    parentChatId: "p",
    spawnLabel: "edit",
    subagentState: "failed",
  },
];

describe("SessionList.vue subagent nesting", () => {
  let root: HTMLElement;
  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  const mount = (viewMode: "list" | "agent") => {
    const app = createApp({
      render: () => h(SessionList, { sessions, activeSessionId: null, viewMode }),
    });
    app.use(i18n);
    app.mount(root);
    return app;
  };

  it("nests children under the parent with label, mode and state", () => {
    const app = mount("list");
    const items = Array.from(root.querySelectorAll("[data-depth]")) as HTMLElement[];
    expect(items.map((i) => i.dataset.depth)).toEqual(["0", "1", "1"]);
    expect(items[1].querySelector('[data-testid="session-title"]')?.textContent).toBe("review");
    expect(items[1].querySelector('[data-testid="subagent-mode"]')?.textContent).toBe("read-only");
    expect(items[1].querySelector('[data-testid="subagent-state"]')?.textContent).toBe("running");
    expect(items[2].querySelector('[data-testid="subagent-mode"]')?.textContent).toBe("writable");
    expect(items[2].querySelector('[data-testid="subagent-state"]')?.textContent).toBe("failed");
    app.unmount();
  });

  it("collapses and expands the children of a parent", async () => {
    const app = mount("list");
    const toggle = () =>
      (root.querySelector('[data-testid="session-toggle-children"]') as HTMLElement).click();
    toggle();
    await nextTick();
    expect(root.querySelectorAll("[data-depth]")).toHaveLength(1);
    toggle();
    await nextTick();
    expect(root.querySelectorAll("[data-depth]")).toHaveLength(3);
    app.unmount();
  });

  it("keeps children in the parent's agent group in agent mode", () => {
    const app = mount("agent");
    expect(root.textContent).toContain("lead");
    // Children use other agents but are not grouped as top-level chats.
    expect(root.textContent).not.toMatch(/reviewer\s*\(1\)/);
    expect(root.querySelectorAll("[data-depth]")).toHaveLength(3);
    app.unmount();
  });
});
