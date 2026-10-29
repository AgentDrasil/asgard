// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import RunnersPanel from "./RunnersPanel.vue";
import { i18n, setLocale } from "../i18n";
import * as api from "../lib/api";
import { resetRunnersForTest } from "../composables/useRunners";

vi.mock("@iconify/vue", () => ({
  Icon: { name: "Icon", props: ["icon"], template: `<span class="mock-icon"></span>` },
}));

const flush = async () => {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
    await nextTick();
  }
};

describe("RunnersPanel", () => {
  let root: HTMLElement;
  let app: ReturnType<typeof createApp>;

  beforeEach(() => {
    setLocale("en", false);
    resetRunnersForTest();
    root = document.createElement("div");
    document.body.appendChild(root);
  });
  afterEach(() => {
    app?.unmount();
    document.body.removeChild(root);
    vi.restoreAllMocks();
  });

  it("shows each runner with status, last seen, versions and refreshes", async () => {
    const spy = vi.spyOn(api, "getRunners").mockResolvedValue({
      backend_version: "backend-9",
      runners: [
        {
          node_id: "node-a",
          online: true,
          last_seen: new Date(Date.now() - 5 * 60_000).toISOString(),
          version: "runner-3",
        },
        { node_id: "node-b", online: false },
      ],
    });
    app = createApp({ render: () => h(RunnersPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    const rows = root.querySelectorAll('[data-testid="runner-row"]');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain("node-a");
    expect(rows[0].textContent).toContain("Online");
    expect(rows[0].textContent).toContain("5m ago");
    expect(rows[0].textContent).toContain("runner-3");
    expect(rows[1].textContent).toContain("Offline");
    expect(rows[1].textContent).toContain("never");
    expect(root.querySelector('[data-testid="backend-version"]')!.textContent).toContain(
      "backend-9",
    );

    root.querySelector<HTMLButtonElement>('[data-testid="runners-refresh"]')!.click();
    await flush();
    expect(spy).toHaveBeenCalledTimes(2);
  });
});
