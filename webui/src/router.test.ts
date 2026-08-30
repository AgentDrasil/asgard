// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { routes } from "./router";
import SettingsView from "./views/SettingsView.vue";
import ConfigEditView from "./views/ConfigEditView.vue";
import LogView from "./views/LogView.vue";

describe("router configuration", () => {
  it("registers routes for /settings, /settings/config, and /settings/logs with actual components", () => {
    const settingsRoute = routes.find((r) => r.path === "/settings");
    const configRoute = routes.find((r) => r.path === "/settings/config");
    const logsRoute = routes.find((r) => r.path === "/settings/logs");

    expect(settingsRoute).toBeDefined();
    expect(settingsRoute?.name).toBe("settings");
    expect(settingsRoute?.component).toBe(SettingsView);

    expect(configRoute).toBeDefined();
    expect(configRoute?.name).toBe("settings-config");
    expect(configRoute?.component).toBe(ConfigEditView);

    expect(logsRoute).toBeDefined();
    expect(logsRoute?.name).toBe("settings-logs");
    expect(logsRoute?.component).toBe(LogView);
  });
});
