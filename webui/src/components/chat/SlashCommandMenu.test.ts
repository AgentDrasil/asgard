// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { createApp, ref, h, nextTick } from "vue";
import SlashCommandMenu from "./SlashCommandMenu.vue";
import * as api from "../../lib/api";
import { i18n } from "../../i18n";
import { __resetProxyEnvsCacheForTest } from "../../composables/useProxyEnvs";

describe("SlashCommandMenu.vue", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    __resetProxyEnvsCacheForTest();
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

  it("prevents parent Enter handler from firing when menu is open (capture phase)", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
    });

    const root = document.createElement("div");
    document.body.appendChild(root);

    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);

    let parentEnterFired = false;
    textarea.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        parentEnterFired = true;
      }
    });

    const textModel = ref("/");
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
    await menuRef?.openMenu();
    await nextTick();

    expect(menuRef?.isOpen).toBe(true);

    // Press Enter while menu is open
    const enterEvt = new KeyboardEvent("keydown", {
      key: "Enter",
      bubbles: true,
      cancelable: true,
    });
    textarea.dispatchEvent(enterEvt);
    await nextTick();

    // Parent listener should NOT have received this enter
    expect(parentEnterFired).toBe(false);

    app.unmount();
  });

  it("does not trigger menu during IME composition", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
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

    // Fire keydown with isComposing = true
    const imeEvent = new KeyboardEvent("keydown", { key: "/", isComposing: true, bubbles: true });
    textarea.dispatchEvent(imeEvent);
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();

    expect(menuRef?.isOpen).toBe(false);

    app.unmount();
  });

  it("does not trigger menu when slash is inside a word/URL", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
    });

    const root = document.createElement("div");
    document.body.appendChild(root);

    const textarea = document.createElement("textarea");
    textarea.value = "https:/";
    textarea.setSelectionRange(7, 7);
    document.body.appendChild(textarea);

    const textModel = ref("https:/");
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

    const slashEvent = new KeyboardEvent("keydown", { key: "/", bubbles: true });
    textarea.dispatchEvent(slashEvent);
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();

    expect(menuRef?.isOpen).toBe(false);

    app.unmount();
  });

  it("filters env items dynamically as user types", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_KEY"],
    });

    const root = document.createElement("div");
    document.body.appendChild(root);

    const textarea = document.createElement("textarea");
    textarea.value = "";
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

    // Type /
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    await menuRef?.openMenu();
    menuRef.slashIndex = 0;
    await nextTick();
    expect(menuRef?.isOpen).toBe(true);

    // Switch to env view
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await nextTick();

    // Now type 'OPEN' -> value becomes "/OPEN"
    textarea.value = "/OPEN";
    textarea.setSelectionRange(5, 5);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    const menuEl = document.querySelector('[data-testid="slash-command-menu"]');
    expect(menuEl?.textContent).toContain("OPENAI_API_KEY");
    expect(menuEl?.textContent).not.toContain("GEMINI_API_KEY");

    app.unmount();
  });

  it("filters immediately when user types characters right after typing slash", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY", "OPENAI_API_KEY"],
    });

    const root = document.createElement("div");
    document.body.appendChild(root);

    const textarea = document.createElement("textarea");
    textarea.value = "";
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

    // User presses / (keydown fires before the character is inserted)
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));

    // The "/" character lands
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));

    // User immediately types "en"
    textarea.value = "/en";
    textarea.setSelectionRange(3, 3);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));

    await nextTick();

    expect(menuRef?.isOpen).toBe(true);
    const menuEl = document.querySelector('[data-testid="slash-command-menu"]');
    expect(menuEl?.textContent).toContain("env");

    app.unmount();
  });

  it("opens immediately and applies typed filter while /api/config is still pending", async () => {
    type BackendConfig = Awaited<ReturnType<typeof api.getBackendConfig>>;
    let resolveCfg!: (cfg: BackendConfig) => void;
    vi.spyOn(api, "getBackendConfig").mockImplementation(
      () =>
        new Promise<BackendConfig>((res) => {
          resolveCfg = res;
        }),
    );

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

    // User presses / while the config request will hang
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    await nextTick();

    // Menu must be open immediately, without waiting for the network
    expect(menuRef?.isOpen).toBe(true);
    expect(document.querySelector('[data-testid="slash-command-menu"]')).not.toBeNull();

    // User keeps typing while the request is in flight
    textarea.value = "/en";
    textarea.setSelectionRange(3, 3);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    const menuEl = document.querySelector('[data-testid="slash-command-menu"]');
    // Filter query is reflected in the breadcrumb
    expect(menuEl?.textContent).toContain(":en");
    expect(menuEl?.textContent).toContain("env");

    // Config finally resolves; menu stays open and items reactively appear
    resolveCfg({ proxy_envs: ["GEMINI_API_KEY"] });
    await new Promise((r) => setTimeout(r, 0));
    await nextTick();
    expect(menuRef?.isOpen).toBe(true);

    app.unmount();
  });

  it("opens on mobile virtual keyboard input event without keydown", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
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

    // Mobile virtual keyboard inserts text without keydown e.key === "/"
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", bubbles: true }));
    await nextTick();

    expect(menuRef?.isOpen).toBe(true);
    expect(document.querySelector('[data-testid="slash-command-menu"]')).not.toBeNull();

    app.unmount();
  });

  it("guards against IME composition and opens after compositionend", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
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

    // Start IME composition
    textarea.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));

    // Input event fired during composition with slash
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    // Menu should NOT open during composition
    expect(menuRef?.isOpen).toBe(false);

    // End IME composition
    textarea.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
    await nextTick();

    // Menu should now open
    expect(menuRef?.isOpen).toBe(true);

    app.unmount();
  });

  it("reopens menu when user backspaces/edits back to slash", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
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

    // Type /abc which has no matching root command -> closes menu
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", bubbles: true }));
    await nextTick();
    expect(menuRef?.isOpen).toBe(true);

    textarea.value = "/abc";
    textarea.setSelectionRange(4, 4);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();
    expect(menuRef?.isOpen).toBe(false);

    // Now user backspaces back to "/" (cursor at 1)
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    expect(menuRef?.isOpen).toBe(true);

    app.unmount();
  });

  it("automatically closes when typed query has no matches", async () => {
    vi.spyOn(api, "getBackendConfig").mockResolvedValue({
      proxy_envs: ["GEMINI_API_KEY"],
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

    // Type "/" to open menu
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", bubbles: true }));
    await nextTick();
    expect(menuRef?.isOpen).toBe(true);

    // Type "aa" (value="/aa") which does not match any item ("env")
    textarea.value = "/aa";
    textarea.setSelectionRange(3, 3);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    // Menu should automatically close without Escape or outside click
    expect(menuRef?.isOpen).toBe(false);
    expect(document.querySelector('[data-testid="slash-command-menu"]')).toBeNull();

    app.unmount();
  });

  it("keeps menu open and displays matching env items when typing /env:prefix while /api/config is pending", async () => {
    type BackendConfig = Awaited<ReturnType<typeof api.getBackendConfig>>;
    let resolveCfg!: (cfg: BackendConfig) => void;
    vi.spyOn(api, "getBackendConfig").mockImplementation(
      () =>
        new Promise<BackendConfig>((res) => {
          resolveCfg = res;
        }),
    );

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

    // User types / to open menu
    textarea.value = "/";
    textarea.setSelectionRange(1, 1);
    textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", bubbles: true }));
    await nextTick();

    expect(menuRef?.isOpen).toBe(true);

    // User immediately types "env:GE" while getBackendConfig is still in flight
    textarea.value = "/env:GE";
    textarea.setSelectionRange(7, 7);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();

    // Menu MUST NOT auto-close while env options are still loading
    expect(menuRef?.isOpen).toBe(true);
    let menuEl = document.querySelector('[data-testid="slash-command-menu"]');
    expect(menuEl).not.toBeNull();
    expect(menuEl?.textContent).toContain(":GE");

    // Backend config resolves with matching GEMINI_API_KEY
    resolveCfg({ proxy_envs: ["GEMINI_API_KEY", "OPENAI_API_KEY"] });
    await new Promise((r) => setTimeout(r, 0));
    await nextTick();

    expect(menuRef?.isOpen).toBe(true);
    menuEl = document.querySelector('[data-testid="slash-command-menu"]');
    expect(menuEl?.textContent).toContain("GEMINI_API_KEY");
    expect(menuEl?.textContent).not.toContain("OPENAI_API_KEY");

    app.unmount();
  });
});
