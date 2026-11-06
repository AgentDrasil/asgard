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

  it("shows the account alias next to the model name when present", async () => {
    await mount({
      claude: [
        { model: "m1", remaining: 0.5, account: "work" },
        { model: "m1", remaining: 0.9, account: "home" },
        { model: "m2", remaining: 1 },
      ],
    });
    const titles = Array.from(root.querySelectorAll("h4")).map((h) => h.textContent?.trim());
    expect(titles).toEqual(["m1 · work", "m1 · home", "m2"]);
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
  it("explains no credentials with the runner id and a login hint (aggregate)", async () => {
    await mount({
      runners: {
        n1: { agy: [{ status: "no_credentials", message: "Authentication required" }] },
        n2: { claude: [model] },
      },
    });
    const el = root.querySelector('[data-testid="quota-status-no_credentials"]')!;
    expect(el.textContent).toContain("No agy credentials on runner n1.");
    expect(el.textContent).toContain("aw agy login NAME");
    expect(el.textContent).toContain("Authentication required");
    expect(root.querySelectorAll("h4").length).toBe(1);
  });

  it("uses the selected runner for no credentials in the flat shape", async () => {
    vi.spyOn(api, "getRunners").mockResolvedValue({
      backend_version: "",
      runners: [{ node_id: "n9", online: true }],
    });
    await useRunners().refreshRunners();
    await mount({ claude: [{ status: "no_credentials", message: "Not logged in" }] });
    expect(root.textContent).toContain("No claude credentials on runner n9.");
  });

  it("shows per-CLI query errors with their text next to working models", async () => {
    await mount({
      agy: [{ status: "error", message: "bwrap: Permission denied", account: "work" }],
      claude: [model],
    });
    const el = root.querySelector('[data-testid="quota-status-error"]')!;
    expect(el.textContent).toContain("Failed to query agy (work) quota.");
    expect(el.textContent).toContain("bwrap: Permission denied");
    expect(root.textContent).toContain("m1");
  });

  it("renders the Chinese no-credentials text", async () => {
    setLocale("zh-CN", false);
    await mount({ agy: [{ status: "no_credentials" }] });
    expect(root.textContent).toContain("agy 凭据");
    setLocale("en", false);
  });

  it("filters out zero balances for DeepSeek and only renders positive currencies", async () => {
    await mount({
      simplest: [
        {
          model: "deepseek/dsv4-flash",
          remaining: 1,
          balance: {
            is_available: true,
            balance_infos: [
              {
                currency: "USD",
                total_balance: "0.00",
                granted_balance: "0.00",
                topped_up_balance: "0.00",
              },
              {
                currency: "CNY",
                total_balance: "76.62",
                granted_balance: "0.00",
                topped_up_balance: "76.62",
              },
            ],
          },
        },
      ],
    });
    expect(root.textContent).toContain("CNY");
    expect(root.textContent).toContain("76.62");
    expect(root.textContent).not.toContain("USD");
  });

  it("renders no balance when all currencies are zero", async () => {
    await mount({
      simplest: [
        {
          model: "deepseek/dsv4-flash",
          remaining: 0,
          balance: {
            is_available: true,
            balance_infos: [
              {
                currency: "USD",
                total_balance: "0.00",
                granted_balance: "0.00",
                topped_up_balance: "0.00",
              },
              {
                currency: "CNY",
                total_balance: "0.00",
                granted_balance: "0.00",
                topped_up_balance: "0.00",
              },
            ],
          },
        },
      ],
    });
    expect(root.textContent).toContain("No balance");
    expect(root.querySelector('[data-testid="quota-no-balance"]')).not.toBeNull();
    expect(root.textContent).not.toContain("USD");
    expect(root.textContent).not.toContain("CNY");
  });
});
