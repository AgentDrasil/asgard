// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, nextTick } from "vue";
import ToastContainer from "./ToastContainer.vue";
import { useToast } from "../../composables/useToast";

describe("ToastContainer component", () => {
  beforeEach(() => {
    const { clear } = useToast();
    clear();
    document.body.innerHTML = "";
  });

  it("renders toasts from useToast composable and supports manual dismiss", async () => {
    const { error, toasts } = useToast();

    const root = document.createElement("div");
    document.body.appendChild(root);

    const app = createApp(ToastContainer);
    app.mount(root);

    error("Backend connection degraded: missing token", { title: "Auth Degraded" });
    await nextTick();

    expect(toasts.value).toHaveLength(1);
    const alertEl = document.querySelector(".alert-error");
    expect(alertEl).not.toBeNull();
    expect(alertEl?.textContent).toContain("Auth Degraded");
    expect(alertEl?.textContent).toContain("Backend connection degraded: missing token");

    const closeBtn = alertEl?.querySelector('button[aria-label="Close notification"]');
    expect(closeBtn).not.toBeNull();

    closeBtn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();

    expect(toasts.value).toHaveLength(0);
    expect(document.querySelector(".alert-error")).toBeNull();

    app.unmount();
  });

  it("supports copying toast message to clipboard", async () => {
    const writeTextMock = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, {
      clipboard: {
        writeText: writeTextMock,
      },
    });

    const { error } = useToast();

    const root = document.createElement("div");
    document.body.appendChild(root);

    const app = createApp(ToastContainer);
    app.mount(root);

    error("docker exec -it asgard-cli auth --token test", { title: "Auth Degraded" });
    await nextTick();

    const copyBtn = document.querySelector('button[aria-label="Copy notification"]');
    expect(copyBtn).not.toBeNull();

    copyBtn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTick();

    expect(writeTextMock).toHaveBeenCalledWith("docker exec -it asgard-cli auth --token test");

    app.unmount();
  });
});
