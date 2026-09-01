// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { routes } from "./router";
import SettingsView from "./views/SettingsView.vue";
import KeyBindingsView from "./views/KeyBindingsView.vue";
import ConfigEditView from "./views/ConfigEditView.vue";
import LogView from "./views/LogView.vue";

describe("router configuration", () => {
  it("registers routes for /settings, /settings/keybindings, /settings/config, and /settings/logs with actual components", () => {
    const settingsRoute = routes.find((r) => r.path === "/settings");
    const keybindingsRoute = routes.find((r) => r.path === "/settings/keybindings");
    const configRoute = routes.find((r) => r.path === "/settings/config");
    const logsRoute = routes.find((r) => r.path === "/settings/logs");

    expect(settingsRoute).toBeDefined();
    expect(settingsRoute?.name).toBe("settings");
    expect(settingsRoute?.component).toBe(SettingsView);

    expect(keybindingsRoute).toBeDefined();
    expect(keybindingsRoute?.name).toBe("settings-keybindings");
    expect(keybindingsRoute?.component).toBe(KeyBindingsView);

    expect(configRoute).toBeDefined();
    expect(configRoute?.name).toBe("settings-config");
    expect(configRoute?.component).toBe(ConfigEditView);

    expect(logsRoute).toBeDefined();
    expect(logsRoute?.name).toBe("settings-logs");
    expect(logsRoute?.component).toBe(LogView);
  });
});
