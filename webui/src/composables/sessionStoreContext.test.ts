// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { createApp, defineComponent, h, nextTick } from "vue";
import { useInjectedSessionStore } from "./sessionStoreContext";
import { mountWithSessionStore } from "../test/sessionStore";

const Probe = defineComponent({
  setup() {
    const store = useInjectedSessionStore();
    return () => h("div", { "data-busy": String(store.isInputBusy.value) });
  },
});

describe("session store injection", () => {
  it("hands the provided store to a descendant", async () => {
    const { store, root, unmount } = mountWithSessionStore(Probe);
    expect(root.firstElementChild?.getAttribute("data-busy")).toBe("false");
    store.isRunning.value = true;
    await nextTick();
    expect(root.firstElementChild?.getAttribute("data-busy")).toBe("true");
    unmount();
  });

  it("throws a clear error when no store is provided", () => {
    const app = createApp(Probe);
    const errors: unknown[] = [];
    app.config.errorHandler = (e) => errors.push(e);
    vi.spyOn(console, "warn").mockImplementation(() => {});
    app.mount(document.createElement("div"));
    expect(String(errors[0])).toMatch(/Session store not provided/);
  });
});
