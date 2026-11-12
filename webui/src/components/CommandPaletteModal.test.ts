// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import CommandPaletteModal from "./CommandPaletteModal.vue";
import { i18n, setLocale } from "../i18n";
import type { CommandItem } from "../types";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("CommandPaletteModal.vue", () => {
  let root: HTMLElement;
  const commands = [
    { id: "a", title: "Alpha", action: () => {} },
    { id: "b", title: "Beta", action: () => {} },
  ] as unknown as CommandItem[];

  beforeEach(() => {
    setLocale("en", false);
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    document.body.removeChild(root);
  });

  it("exposes combobox/listbox ARIA with active descendant and live count", async () => {
    const app = createApp({
      render() {
        return h(CommandPaletteModal, { isOpen: true, commands });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const input = root.querySelector("input") as HTMLInputElement;
    expect(input.getAttribute("role")).toBe("combobox");
    expect(input.getAttribute("aria-autocomplete")).toBe("list");
    expect(input.getAttribute("aria-expanded")).toBe("true");
    const listbox = root.querySelector('[role="listbox"]') as HTMLElement;
    expect(listbox.id).toBe(input.getAttribute("aria-controls"));
    const options = root.querySelectorAll('[role="option"]');
    expect(options.length).toBe(2);
    expect(input.getAttribute("aria-activedescendant")).toBe(options[0].id);
    expect(options[0].getAttribute("aria-selected")).toBe("true");

    input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }));
    await nextTick();
    expect(input.getAttribute("aria-activedescendant")).toBe(options[1].id);
    expect(options[1].getAttribute("aria-selected")).toBe("true");
    expect(root.querySelector('[aria-live="polite"]')?.textContent).toContain(
      "2 commands available",
    );

    input.value = "zzz";
    input.dispatchEvent(new Event("input"));
    await nextTick();
    expect(input.getAttribute("aria-expanded")).toBe("false");
    expect(input.hasAttribute("aria-activedescendant")).toBe(false);
    expect(root.querySelector('[aria-live="polite"]')?.textContent).toContain("No commands found");
    app.unmount();
  });
});
