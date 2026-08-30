// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { routes, SettingsPlaceholder, ConfigEditPlaceholder } from "./router";

describe("router placeholder components", () => {
  it("executes SettingsPlaceholder functional component returning h() VNode", () => {
    const vnode = SettingsPlaceholder();
    expect(vnode.type).toBe("div");
    expect(vnode.props?.class).toBe("p-6 text-base-content/70");
    expect(vnode.children).toBe("Settings");
  });

  it("executes ConfigEditPlaceholder functional component returning h() VNode", () => {
    const vnode = ConfigEditPlaceholder();
    expect(vnode.type).toBe("div");
    expect(vnode.props?.class).toBe("p-6 text-base-content/70");
    expect(vnode.children).toBe("Config Editor");
  });

  it("registers routes for /settings, /settings/config, and /settings/logs", () => {
    const settingsRoute = routes.find((r) => r.path === "/settings");
    const configRoute = routes.find((r) => r.path === "/settings/config");
    const logsRoute = routes.find((r) => r.path === "/settings/logs");

    expect(settingsRoute).toBeDefined();
    expect(settingsRoute?.name).toBe("settings");

    expect(configRoute).toBeDefined();
    expect(configRoute?.name).toBe("settings-config");

    expect(logsRoute).toBeDefined();
    expect(logsRoute?.name).toBe("settings-logs");
  });
});
