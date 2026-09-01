// @vitest-environment jsdom
import { describe, it, expect, afterEach } from "vitest";
import { detectClientOS, isMacOS, isWindows, isLinux, OS_DISPLAY_NAMES } from "./platform";

describe("platform detection", () => {
  const originalNavigator = globalThis.navigator;

  afterEach(() => {
    Object.defineProperty(globalThis, "navigator", {
      value: originalNavigator,
      configurable: true,
      writable: true,
    });
  });

  describe("detectClientOS with UA-CH (userAgentData)", () => {
    it("detects mac from userAgentData.platform", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          userAgentData: { platform: "macOS" },
          platform: "Win32",
          userAgent: "Mozilla/5.0 (Windows NT 10.0)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("mac");
      expect(isMacOS()).toBe(true);
      expect(isWindows()).toBe(false);
      expect(isLinux()).toBe(false);
    });

    it("detects windows from userAgentData.platform", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          userAgentData: { platform: "Windows" },
          platform: "MacIntel",
          userAgent: "Mozilla/5.0 (Macintosh)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("windows");
      expect(isWindows()).toBe(true);
      expect(isMacOS()).toBe(false);
      expect(isLinux()).toBe(false);
    });

    it("detects linux from userAgentData.platform", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          userAgentData: { platform: "Linux" },
          platform: "Win32",
          userAgent: "Mozilla/5.0",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("linux");
      expect(isLinux()).toBe(true);
    });
  });

  describe("detectClientOS fallback to navigator.platform / userAgent", () => {
    it("detects mac from navigator.platform = MacIntel without UA-CH", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          platform: "MacIntel",
          userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("mac");
      expect(isMacOS()).toBe(true);
    });

    it("detects windows from navigator.platform = Win32", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          platform: "Win32",
          userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("windows");
      expect(isWindows()).toBe(true);
    });

    it("detects linux from navigator.platform = Linux x86_64", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          platform: "Linux x86_64",
          userAgent: "Mozilla/5.0 (X11; Linux x86_64)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("linux");
      expect(isLinux()).toBe(true);
    });

    it("detects mac from userAgent fallback when platform is empty", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          platform: "",
          userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("mac");
    });

    it("detects windows from userAgent fallback when platform is empty", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          platform: "",
          userAgent: "Mozilla/5.0 (Windows NT 10.0)",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("windows");
    });

    it("defaults to linux if unknown", () => {
      Object.defineProperty(globalThis, "navigator", {
        value: {
          platform: "Unknown",
          userAgent: "UnknownBrowser/1.0",
        },
        configurable: true,
        writable: true,
      });

      expect(detectClientOS()).toBe("linux");
    });
  });

  describe("OS_DISPLAY_NAMES", () => {
    it("has correct display names", () => {
      expect(OS_DISPLAY_NAMES.mac).toBe("macOS");
      expect(OS_DISPLAY_NAMES.windows).toBe("Windows");
      expect(OS_DISPLAY_NAMES.linux).toBe("Linux");
    });
  });
});
