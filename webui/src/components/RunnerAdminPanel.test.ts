// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick, h } from "vue";
import RunnerAdminPanel from "./RunnerAdminPanel.vue";
import { i18n, setLocale } from "../i18n";
import * as api from "../lib/api";
import { resetRunnersForTest } from "../composables/useRunners";

vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

const flush = async () => {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
    await nextTick();
  }
};

describe("RunnerAdminPanel", () => {
  let root: HTMLElement;
  let app: ReturnType<typeof createApp>;

  beforeEach(() => {
    setLocale("en", false);
    resetRunnersForTest();
    vi.spyOn(api, "getRunners").mockResolvedValue({
      backend_version: "v1.0.0",
      runners: [],
    });
    root = document.createElement("div");
    document.body.appendChild(root);
  });

  afterEach(() => {
    app?.unmount();
    if (root && root.parentNode) {
      document.body.removeChild(root);
    }
    vi.restoreAllMocks();
  });

  it("renders runners, assigned users with chips, and warns about unassigned runners", async () => {
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [
        { node_id: "node-1", online: true, users: ["user-alice"] },
        { node_id: "node-2", online: false, users: [] },
      ],
      users: [
        { subject: "user-alice", name: "Alice Smith", email: "alice@example.com" },
        { subject: "user-bob", name: "Bob Jones", email: "bob@example.com" },
      ],
    });

    app = createApp({ render: () => h(RunnerAdminPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    const rows = root.querySelectorAll('[data-testid="runner-admin-row"]');
    expect(rows.length).toBe(2);

    // Node 1 has Alice assigned
    expect(rows[0].textContent).toContain("node-1");
    expect(rows[0].textContent).toContain("Online");
    const chips = rows[0].querySelectorAll('[data-testid="assigned-user-chip"]');
    expect(chips.length).toBe(1);
    expect(chips[0].textContent).toContain("Alice Smith");
    expect(chips[0].textContent).toContain("alice@example.com");
    expect(chips[0].textContent).toContain("AS"); // Initials

    // Node 2 is unassigned
    expect(rows[1].textContent).toContain("node-2");
    expect(rows[1].textContent).toContain("Offline");
    expect(rows[1].textContent).toContain("Not assigned to any user");
  });

  it("filters available users via search and assigns a user", async () => {
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [{ node_id: "node-1", online: true, users: [] }],
      users: [
        { subject: "user-alice", name: "Alice Smith", email: "alice@example.com" },
        { subject: "user-bob", name: "Bob Jones", email: "bob@example.com" },
      ],
    });
    const setUsersSpy = vi.spyOn(api, "setRunnerUsers").mockResolvedValue(null);

    app = createApp({ render: () => h(RunnerAdminPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    // Open the popover, then type to filter
    const popover = root.querySelector<HTMLElement>('[data-testid="user-search-dropdown"]')!;
    popover.showPopover();
    const input = root.querySelector<HTMLInputElement>('[data-testid="search-users-input"]');
    expect(input).not.toBeNull();
    input!.focus();
    input!.value = "bob";
    input!.dispatchEvent(new Event("input"));
    await flush();

    // Dropdown should be visible and only show Bob
    const dropdown = root.querySelector('[data-testid="user-search-dropdown"]');
    expect(dropdown).not.toBeNull();
    expect(dropdown!.querySelector('[role="listbox"]')).not.toBeNull();
    expect(dropdown!.querySelector('[role="option"]')).not.toBeNull();
    expect(dropdown!.textContent).toContain("Bob Jones");
    expect(dropdown!.textContent).not.toContain("Alice Smith");

    // Click to add Bob
    const addBtn = dropdown!.querySelector<HTMLButtonElement>('[data-testid="user-option"]');
    expect(addBtn).not.toBeNull();
    addBtn!.click();
    await flush();

    expect(setUsersSpy).toHaveBeenCalledWith("node-1", ["user-bob"]);
  });

  it("removes an assigned user when remove button is clicked", async () => {
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [{ node_id: "node-1", online: true, users: ["user-alice", "user-bob"] }],
      users: [
        { subject: "user-alice", name: "Alice Smith", email: "alice@example.com" },
        { subject: "user-bob", name: "Bob Jones", email: "bob@example.com" },
      ],
    });
    const setUsersSpy = vi.spyOn(api, "setRunnerUsers").mockResolvedValue(null);

    app = createApp({ render: () => h(RunnerAdminPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    const removeBtns = root.querySelectorAll<HTMLButtonElement>('[data-testid="remove-user-btn"]');
    expect(removeBtns.length).toBe(2);

    // Remove Alice
    removeBtns[0].click();
    await flush();

    expect(setUsersSpy).toHaveBeenCalledWith("node-1", ["user-bob"]);
  });

  it("wires the trigger to a native popover and closes it after selecting", async () => {
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [
        { node_id: "node-1", online: true, users: [] },
        { node_id: "node-2", online: true, users: [] },
      ],
      users: [{ subject: "user-alice", name: "Alice", email: "alice@example.com" }],
    });
    vi.spyOn(api, "setRunnerUsers").mockResolvedValue(null);

    app = createApp({ render: () => h(RunnerAdminPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    const triggers = root.querySelectorAll<HTMLElement>('[data-testid="add-user-btn"]');
    const popovers = root.querySelectorAll<HTMLElement>('[data-testid="user-search-dropdown"]');
    expect(popovers.length).toBe(2);
    expect(popovers[0].getAttribute("popover")).toBe("auto");
    expect(popovers[0].id).not.toBe(popovers[1].id);
    expect(triggers[0].getAttribute("popovertarget")).toBe(popovers[0].id);
    expect(triggers[1].getAttribute("popovertarget")).toBe(popovers[1].id);

    triggers[0].click();
    await flush();
    expect(popovers[0].hasAttribute("data-popover-open")).toBe(true);
    expect(popovers[0].querySelector('[role="listbox"]')).not.toBeNull();

    popovers[0].querySelector<HTMLButtonElement>('[data-testid="user-option"]')!.click();
    await flush();
    expect(popovers[0].hasAttribute("data-popover-open")).toBe(false);
  });

  it("renders user avatar image using appendAccessToken when avatarUrl is present", async () => {
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [{ node_id: "node-1", online: true, users: ["user-alice"] }],
      users: [
        {
          subject: "user-alice",
          name: "Alice Smith",
          email: "alice@example.com",
          avatarUrl: "/api/manage/users/user-alice/avatar",
        },
      ],
    });

    app = createApp({ render: () => h(RunnerAdminPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    const chip = root.querySelector('[data-testid="assigned-user-chip"]');
    expect(chip).not.toBeNull();
    const img = chip?.querySelector("img");
    expect(img).not.toBeNull();
    expect(img?.getAttribute("src")).toContain("/api/manage/users/user-alice/avatar");
  });

  it("falls back to initials when avatar image fails to load", async () => {
    vi.spyOn(api, "getManageRunners").mockResolvedValue({
      runners: [{ node_id: "node-1", online: true, users: ["user-alice"] }],
      users: [
        {
          subject: "user-alice",
          name: "Alice Smith",
          email: "alice@example.com",
          avatarUrl: "/api/manage/users/user-alice/avatar",
        },
      ],
    });

    app = createApp({ render: () => h(RunnerAdminPanel) });
    app.use(i18n);
    app.mount(root);
    await flush();

    const chip = root.querySelector('[data-testid="assigned-user-chip"]');
    expect(chip).not.toBeNull();
    const img = chip?.querySelector("img");
    expect(img).not.toBeNull();

    img?.dispatchEvent(new Event("error"));
    await flush();

    expect(chip?.querySelector("img")).toBeNull();
    expect(chip?.textContent).toContain("AS");
  });
});
