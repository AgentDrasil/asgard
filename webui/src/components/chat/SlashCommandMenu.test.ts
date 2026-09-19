// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { createApp, ref, h, nextTick } from "vue";
import SlashCommandMenu from "./SlashCommandMenu.vue";
import * as api from "../../lib/api";
import { i18n } from "../../i18n";

describe("SlashCommandMenu.vue", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("opens on slash key and inserts selected env command", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY", "OPENAI_API_KEY"],
    });

    const root = document.createElement("div");
    document.body.appendChild(root);

    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);

    const textModel = ref("");
    let menuRef: any = null;

    const app = createApp({
      setup() {
        return () =>
          h(SlashCommandMenu, {
            ref: (el: any) => {
              menuRef = el;
            },
            targetElement: textarea,
            modelValue: textModel.value,
            "onUpdate:modelValue": (val: string) => {
              textModel.value = val;
            },
          });
      },
    });
    app.use(i18n);
    app.mount(root);

    await nextTick();

    // Trigger open
    await menuRef?.openMenu();
    await nextTick();

    expect(menuRef?.isOpen).toBe(true);

    const menuEl = document.querySelector('[data-testid="slash-command-menu"]');
    expect(menuEl).not.toBeNull();
    expect(menuEl?.textContent).toContain("env");

    // Press Enter to select 'env'
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await nextTick();

    // Menu should now show proxy env items
    expect(menuEl?.textContent).toContain("GEMINI_API_KEY");

    // Press Enter again to select GEMINI_API_KEY
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await nextTick();

    expect(textModel.value).toContain("/env:GEMINI_API_KEY ");
    expect(menuRef?.isOpen).toBe(false);

    app.unmount();
  });
});
