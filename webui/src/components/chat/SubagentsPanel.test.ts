// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import SubagentsPanel from "./SubagentsPanel.vue";
import type { ChatSession } from "../../types";
import { i18n } from "../../i18n";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

const child = (id: string, over: Partial<ChatSession> = {}): ChatSession => ({
  chatID: id,
  title: "",
  currentAgent: "reviewer",
  runDir: "/ws",
  parentChatId: "p",
  spawnLabel: id,
  subagentState: "running",
  ...over,
});

describe("SubagentsPanel.vue", () => {
  let root: HTMLElement;
  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  const mount = (props: any, on: Record<string, any> = {}) => {
    const app = createApp({ render: () => h(SubagentsPanel, { ...props, ...on }) });
    app.use(i18n);
    app.mount(root);
    return app;
  };

  it("renders nothing without subagents", () => {
    const app = mount({ subagents: [] });
    expect(root.querySelector('[data-testid="subagents-panel"]')).toBeNull();
    app.unmount();
  });

  it("lists children with state and mode, and offers kill only for live ones", () => {
    const app = mount({
      subagents: [
        child("a", { subagentState: "running", spawnReadonly: true }),
        child("b", { subagentState: "queued" }),
        child("c", { subagentState: "done" }),
        child("d", { subagentState: "killed" }),
      ],
    });
    const rows = root.querySelectorAll('[data-testid="subagent-row"]');
    expect(rows).toHaveLength(4);
    expect(rows[0].textContent).toContain("running");
    expect(rows[0].textContent).toContain("read-only");
    expect(rows[1].textContent).toContain("queued");
    expect(rows[1].textContent).toContain("writable");
    expect(rows[2].textContent).toContain("done");
    expect(root.querySelectorAll('[data-testid="subagent-kill"]')).toHaveLength(2);
    app.unmount();
  });

  it("emits open and kill with the child id", () => {
    const open = vi.fn<(id: string) => void>();
    const kill = vi.fn<(id: string) => void>();
    const app = mount({ subagents: [child("a")] }, { onOpen: open, onKill: kill });
    (root.querySelector('[data-testid="subagent-open"]') as HTMLElement).click();
    (root.querySelector('[data-testid="subagent-kill"]') as HTMLElement).click();
    expect(open).toHaveBeenCalledWith("a");
    expect(kill).toHaveBeenCalledWith("a");
    app.unmount();
  });

  it("disables kill while a request is in flight and collapses on toggle", async () => {
    const app = mount({ subagents: [child("a")], killingIds: ["a"] });
    expect(
      (root.querySelector('[data-testid="subagent-kill"]') as HTMLButtonElement).disabled,
    ).toBe(true);
    (root.querySelector('[data-testid="subagents-panel-toggle"]') as HTMLElement).click();
    await nextTick();
    expect(root.querySelectorAll('[data-testid="subagent-row"]')).toHaveLength(0);
    app.unmount();
  });
});
