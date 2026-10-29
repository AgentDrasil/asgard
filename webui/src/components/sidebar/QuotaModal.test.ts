// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import QuotaModal from "./QuotaModal.vue";
import { createMemoryStorage } from "../../test/memoryStorage";
import { i18n, setLocale } from "../../i18n";
import * as api from "../../lib/api";
import { resetRunnersForTest, useRunners } from "../../composables/useRunners";

vi.mock("@iconify/vue", () => ({
  Icon: { name: "Icon", props: ["icon"], template: `<span class="mock-icon"></span>` },
}));

const flush = async () => {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
    await nextTick();
  }
};

const model = { model: "m1", remaining: 0.5 };

describe("QuotaModal", () => {
  let root: HTMLElement;
  let app: ReturnType<typeof createApp>;

  const mount = async (json: unknown) => {
    const fetchSpy = vi
      .spyOn(api, "apiFetch")
      .mockResolvedValue({ ok: true, status: 200, json: async () => json } as Response);
    app = createApp({ render: () => h(QuotaModal, { modelValue: true }) });
    app.use(i18n);
    app.mount(root);
    await flush();
    return fetchSpy;
  };

  beforeEach(() => {
    setLocale("en", false);
    vi.stubGlobal("localStorage", createMemoryStorage());
    resetRunnersForTest();
    root = document.createElement("div");
    document.body.appendChild(root);
  });
  afterEach(() => {
    app?.unmount();
    document.body.removeChild(root);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("requests ?runner=<selected> and renders the flat shape", async () => {
    vi.spyOn(api, "getRunners").mockResolvedValue({
      backend_version: "",
      runners: [{ node_id: "n1", online: true }],
    });
    await useRunners().refreshRunners();
    const fetchSpy = await mount({ claude: [model] });
    expect(fetchSpy).toHaveBeenCalledWith("/api/quota?runner=n1");
    expect(root.textContent).toContain("claude");
    expect(root.textContent).toContain("m1");
    expect(root.querySelector('[data-testid="quota-runner-section"]')).toBeNull();
  });

  it("calls /api/quota without runner in legacy mode", async () => {
    const fetchSpy = await mount({ claude: [model] });
    expect(fetchSpy).toHaveBeenCalledWith("/api/quota");
  });

  it("renders the aggregate shape per runner with per-runner errors", async () => {
    await mount({
      runners: { n1: { claude: [model] }, n2: { codex: [{ model: "m2", remaining: 0.9 }] } },
      errors: { n3: "boom" },
    });
    const sections = root.querySelectorAll('[data-testid="quota-runner-section"]');
    expect(sections.length).toBe(2);
    expect(sections[0].textContent).toContain("n1");
    expect(sections[1].textContent).toContain("n2");
    expect(root.textContent).toContain("m1");
    expect(root.textContent).toContain("m2");
    expect(root.querySelector('[data-testid="quota-runner-errors"]')!.textContent).toContain(
      "n3: boom",
    );
  });
});
