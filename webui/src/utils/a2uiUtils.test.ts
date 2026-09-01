import { describe, it, expect } from "vitest";
import {
  isA2UIManifestPath,
  isA2UIManifest,
  parseA2UIManifest,
  formatA2UIMoney,
  formatA2UIPercent,
  formatA2UINumber,
  resolveAssetPath,
} from "./a2uiUtils";

describe("a2uiUtils", () => {
  describe("isA2UIManifestPath", () => {
    it("recognizes standard ui_manifest.json filenames", () => {
      expect(isA2UIManifestPath("ui_manifest.json")).toBe(true);
      expect(isA2UIManifestPath("data/output/ui_manifest.json")).toBe(true);
      expect(isA2UIManifestPath("/workspace/reports/ui_manifest.json")).toBe(true);
      expect(isA2UIManifestPath("UI_MANIFEST.JSON")).toBe(true);
      expect(isA2UIManifestPath("ui-manifest.json")).toBe(true);
      expect(isA2UIManifestPath("view.a2ui.json")).toBe(true);
    });

    it("rejects generic manifest paths without a2ui prefix", () => {
      expect(isA2UIManifestPath("manifest.json")).toBe(false);
      expect(isA2UIManifestPath("app.manifest.json")).toBe(false);
      expect(isA2UIManifestPath("package.json")).toBe(false);
      expect(isA2UIManifestPath("data.csv")).toBe(false);
      expect(isA2UIManifestPath(null)).toBe(false);
      expect(isA2UIManifestPath("")).toBe(false);
    });
  });

  describe("isA2UIManifest", () => {
    it("returns true for matching explicit filename even without content", () => {
      expect(isA2UIManifest("ui_manifest.json", "/data/ui_manifest.json")).toBe(true);
    });

    it("returns true for JSON content with schemaVersion 1.0", () => {
      const json = JSON.stringify({
        schemaVersion: "1.0",
        title: "Test Dashboard",
        tabs: [
          {
            id: "tab1",
            label: "Main",
            widgets: [{ id: "w1", type: "chart", labels: [], datasets: [] }],
          },
        ],
      });
      expect(isA2UIManifest("other.json", "/path/other.json", json)).toBe(true);
    });

    it("returns true for JSON content with valid widgets and kpis", () => {
      const json = JSON.stringify({
        title: "Test Dashboard",
        kpis: [{ id: "kpi1", label: "Metric", value: 100 }],
        tabs: [
          {
            id: "tab1",
            label: "Overview",
            widgets: [{ id: "w1", type: "key-val-list", items: [] }],
          },
        ],
      });
      expect(isA2UIManifest("custom.json", "/path/custom.json", json)).toBe(true);
    });

    it("returns false for regular JSON and non-A2UI manifest files", () => {
      const pkgJson = JSON.stringify({
        name: "my-package",
        version: "1.0.0",
        dependencies: {},
      });
      expect(isA2UIManifest("package.json", "/path/package.json", pkgJson)).toBe(false);

      const extManifest = JSON.stringify({
        manifest_version: 3,
        name: "Extension",
        version: "1.0",
        action: { default_title: "Popup" },
      });
      expect(isA2UIManifest("manifest.json", "/path/manifest.json", extManifest)).toBe(false);
    });
  });

  describe("parseA2UIManifest", () => {
    it("parses valid A2UI JSON manifest into typed object", () => {
      const raw = JSON.stringify({
        schemaVersion: "1.0",
        title: "Portfolio Summary",
        subtitle: "Realtime stats",
        asOfDate: "2026-08-31",
        kpis: [
          {
            id: "net-worth",
            label: "Net Worth",
            value: 1250000.5,
            format: "currency",
          },
        ],
        tabs: [
          {
            id: "main",
            label: "Main",
            layout: "grid-2",
            widgets: [
              {
                id: "w1",
                type: "chart",
                chartType: "donut",
                labels: ["US", "Intl"],
                datasets: [{ data: [60, 40] }],
              },
            ],
          },
        ],
      });

      const manifest = parseA2UIManifest(raw);
      expect(manifest).not.toBeNull();
      expect(manifest?.title).toBe("Portfolio Summary");
      expect(manifest?.asOfDate).toBe("2026-08-31");
      expect(manifest?.kpis?.length).toBe(1);
      expect(manifest?.tabs?.length).toBe(1);
      expect(manifest?.tabs?.[0].widgets.length).toBe(1);
    });

    it("returns null on invalid JSON", () => {
      expect(parseA2UIManifest("not a json string")).toBeNull();
      expect(parseA2UIManifest(null)).toBeNull();
    });
  });

  describe("formatting utilities", () => {
    it("formats currency properly", () => {
      expect(formatA2UIMoney(1234567.89)).toBe("$1,234,567.89");
      expect(formatA2UIMoney(0)).toBe("$0.00");
      expect(formatA2UIMoney("5000.5")).toBe("$5,000.50");
      expect(formatA2UIMoney(null)).toBe("$0.00");
    });

    it("formats percentages properly", () => {
      expect(formatA2UIPercent(15.25)).toBe("+15.25%");
      expect(formatA2UIPercent(-3.5)).toBe("-3.50%");
      expect(formatA2UIPercent(0)).toBe("0.00%");
      expect(formatA2UIPercent(null)).toBe("0.00%");
    });

    it("formats numbers properly", () => {
      expect(formatA2UINumber(9876543)).toBe("9,876,543");
      expect(formatA2UINumber(null)).toBe("0");
    });
  });

  describe("resolveAssetPath", () => {
    it("resolves relative path relative to manifest directory", () => {
      expect(resolveAssetPath("positions.csv", "data/output/ui_manifest.json")).toBe(
        "data/output/positions.csv",
      );
      expect(resolveAssetPath("report.md", "/home/user/workspace/output/ui_manifest.json")).toBe(
        "/home/user/workspace/output/report.md",
      );
    });

    it("leaves absolute paths untouched", () => {
      expect(resolveAssetPath("/data/positions.csv", "output/ui_manifest.json")).toBe(
        "/data/positions.csv",
      );
    });
  });
});
