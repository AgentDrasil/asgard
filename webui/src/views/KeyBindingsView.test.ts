// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { createApp, h, nextTick } from "vue";
import KeyBindingsView from "./KeyBindingsView.vue";
import * as api from "../lib/api";
import * as platform from "../utils/platform";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

// Mock vue-router
const mockPush = vi.fn<() => void>();
vi.mock("vue-router", () => ({
  useRouter: () => ({
    push: mockPush,
  }),
}));

describe("KeyBindingsView.vue", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();
    mockPush.mockReset();

    // Default mock platform to linux
    vi.spyOn(platform, "detectClientOS").mockReturnValue("linux");
    vi.spyOn(platform, "isMacOS").mockReturnValue(false);

    // Default mock API
    vi.spyOn(api, "getKeybindings").mockResolvedValue({
      overrides: {},
      exists: false,
    });
    vi.spyOn(api, "saveKeybindings").mockResolvedValue({
      success: true,
    });
  });

  afterEach(() => {
    if (root && root.parentNode) {
      root.parentNode.removeChild(root);
    }
  });

  it("renders with OS selector tabs and shows current OS badge", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    const tabs = root.querySelectorAll(".tabs button.tab");
    expect(tabs.length).toBe(3);

    // First tab is Linux and has Current OS badge
    expect(tabs[0].textContent).toContain("Linux");
    expect(tabs[0].textContent).toContain("Current OS");

    app.unmount();
  });

  it("switches OS tabs and disables edit controls in non-current OS viewing mode", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    const tabs = root.querySelectorAll(".tabs button.tab");
    // Switch to Windows tab (tab[1])
    (tabs[1] as HTMLButtonElement).click();
    await nextTick();

    // Read-only notice should appear
    expect(root.textContent).toContain("Viewing mode only");

    // All record buttons should be disabled
    const recordButtons = root.querySelectorAll('button[title="Record new shortcut"]');
    expect(recordButtons.length).toBeGreaterThan(0);
    recordButtons.forEach((btn) => {
      expect((btn as HTMLButtonElement).disabled).toBe(true);
    });

    // Save button should be disabled
    const saveBtn = root.querySelector("footer button.btn-primary") as HTMLButtonElement;
    expect(saveBtn).not.toBeNull();
    expect(saveBtn.disabled).toBe(true);

    app.unmount();
  });

  it("handles shortcut recording, modifier-only ignore, and key completion", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    // Find the record button for the first action (toggle_sidebar)
    const recordButtons = root.querySelectorAll('button[title="Record new shortcut"]');
    expect(recordButtons.length).toBeGreaterThan(0);

    (recordButtons[0] as HTMLButtonElement).click();
    await nextTick();

    // Pulse badge should show "Press shortcut keys..."
    expect(root.textContent).toContain("Press shortcut keys... (Esc to cancel)");

    // Press pure modifier (Shift) -> should NOT complete recording
    const shiftEvent = new KeyboardEvent("keydown", {
      key: "Shift",
      code: "ShiftLeft",
      shiftKey: true,
      bubbles: true,
    });
    window.dispatchEvent(shiftEvent);
    await nextTick();

    expect(root.textContent).toContain("Press shortcut keys... (Esc to cancel)");

    // Press Shift+K -> should complete recording
    const kEvent = new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      shiftKey: true,
      bubbles: true,
    });
    window.dispatchEvent(kEvent);
    await nextTick();

    // Recording finished, active badge displays Shift+K
    expect(root.textContent).not.toContain("Press shortcut keys... (Esc to cancel)");
    expect(root.textContent).toContain("Shift+K");

    app.unmount();
  });

  it("handles Escape to cancel recording", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    const recordButtons = root.querySelectorAll('button[title="Record new shortcut"]');
    (recordButtons[0] as HTMLButtonElement).click();
    await nextTick();

    expect(root.textContent).toContain("Press shortcut keys... (Esc to cancel)");

    // Press Escape
    const escEvent = new KeyboardEvent("keydown", {
      key: "Escape",
      code: "Escape",
      bubbles: true,
    });
    window.dispatchEvent(escEvent);
    await nextTick();

    expect(root.textContent).not.toContain("Press shortcut keys... (Esc to cancel)");

    app.unmount();
  });

  it("handles Clear button setting shortcut to [] and displaying Unassigned", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    const clearButtons = root.querySelectorAll('button[title="Clear shortcut"]');
    expect(clearButtons.length).toBeGreaterThan(0);

    // Clear first shortcut
    (clearButtons[0] as HTMLButtonElement).click();
    await nextTick();

    expect(root.textContent).toContain("Unassigned");

    app.unmount();
  });

  it("displays conflict warning when shortcuts collide", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    // Record the second action (toggle_diff) to the same key as toggle_sidebar (Ctrl+B)
    const recordButtons = root.querySelectorAll('button[title="Record new shortcut"]');
    (recordButtons[1] as HTMLButtonElement).click();
    await nextTick();

    const ctrlBEvent = new KeyboardEvent("keydown", {
      key: "b",
      code: "KeyB",
      ctrlKey: true,
      bubbles: true,
    });
    window.dispatchEvent(ctrlBEvent);
    await nextTick();

    // Conflict warning should appear
    expect(root.textContent).toContain("Conflicts with:");
    expect(root.textContent).toContain("Toggle Sidebar");

    app.unmount();
  });

  it("disables Save Changes button when config load error occurs (R-2)", async () => {
    vi.spyOn(api, "getKeybindings").mockResolvedValue({
      overrides: {},
      error: "corrupted keys.yaml format",
    });

    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    // Allow loadCustomKeybindings promise to settle
    await new Promise((r) => setTimeout(r, 10));
    await nextTick();

    // Corrupted banner is visible
    expect(root.textContent).toContain("Configuration Loading Error");
    expect(root.textContent).toContain("Saving is disabled to prevent overwriting");

    // Save button is disabled
    const saveBtn = root.querySelector("footer button.btn-primary") as HTMLButtonElement;
    expect(saveBtn).not.toBeNull();
    expect(saveBtn.disabled).toBe(true);

    app.unmount();
  });

  it("cancels recording and ignores keydown events when switching OS tabs (D-1)", async () => {
    const app = createApp({
      render() {
        return h(KeyBindingsView);
      },
    });
    app.mount(root);
    await nextTick();

    // 1. Start recording on current OS (Linux)
    const recordButtons = root.querySelectorAll('button[title="Record new shortcut"]');
    (recordButtons[0] as HTMLButtonElement).click();
    await nextTick();

    expect(root.textContent).toContain("Press shortcut keys... (Esc to cancel)");

    // 2. Switch OS tab to Windows (tabs[1])
    const tabs = root.querySelectorAll(".tabs button.tab");
    (tabs[1] as HTMLButtonElement).click();
    await nextTick();

    // Recording indicator should no longer be present
    expect(root.textContent).not.toContain("Press shortcut keys... (Esc to cancel)");

    // 3. Dispatch keydown while in Windows tab
    const kEvent = new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      shiftKey: true,
      bubbles: true,
    });
    window.dispatchEvent(kEvent);
    await nextTick();

    // 4. Switch back to Linux tab
    (tabs[0] as HTMLButtonElement).click();
    await nextTick();

    // The shortcut should still be the original default (Ctrl+B), not modified by Shift+K
    expect(root.textContent).not.toContain("Shift+K");

    app.unmount();
  });
});
