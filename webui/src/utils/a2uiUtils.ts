import type { A2UIManifest } from "../types/a2ui";
import { getFileContent } from "../lib/api";

const VALID_WIDGET_TYPES: Set<string> = new Set([
  "chart",
  "data-table",
  "holdings-table",
  "key-val-list",
  "markdown",
]);

/**
 * Checks whether a given file path/name matches the explicit A2UI manifest naming convention.
 * Generic .manifest.json (such as web extensions or chrome apps) are excluded to prevent misclassification.
 */
export function isA2UIManifestPath(path?: string | null): boolean {
  if (!path) return false;
  const normalized = path.trim().toLowerCase();
  const baseName = normalized.split("/").pop() || normalized;
  return (
    baseName === "ui_manifest.json" ||
    baseName === "ui-manifest.json" ||
    baseName.endsWith(".a2ui.json")
  );
}

/**
 * Checks whether a given file (by name, path, or content) is an A2UI manifest.
 * Performs strict validation to avoid false positives with other JSON/manifest files.
 */
export function isA2UIManifest(
  name?: string | null,
  path?: string | null,
  content?: string | null,
): boolean {
  const isExplicitName = isA2UIManifestPath(name) || isA2UIManifestPath(path);

  if (!content) {
    return isExplicitName;
  }

  try {
    const trimmed = content.trim();
    if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) return false;
    const parsed = JSON.parse(trimmed);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return false;

    // Strict 1: Explicit A2UI schemaVersion
    const hasA2UISchema =
      parsed.schemaVersion === "1.0" ||
      parsed.schemaVersion === 1 ||
      (typeof parsed.schemaVersion === "string" && parsed.schemaVersion.startsWith("1."));

    // Strict 2: Check for valid tabs structure with known A2UI widget types
    const hasValidTabs =
      Array.isArray(parsed.tabs) &&
      parsed.tabs.some((tab: any) =>
        Array.isArray(tab.widgets) &&
        tab.widgets.some((w: any) => w && typeof w.type === "string" && VALID_WIDGET_TYPES.has(w.type)),
      );

    const hasValidKpis =
      Array.isArray(parsed.kpis) &&
      parsed.kpis.length > 0 &&
      parsed.kpis.some((k: any) => k && (k.label || k.value !== undefined));

    if (hasA2UISchema) {
      return Boolean(hasValidTabs || hasValidKpis || isExplicitName);
    }

    if (isExplicitName && (hasValidTabs || hasValidKpis)) {
      return true;
    }

    // For any other filename, require both valid tabs/widgets or schemaVersion
    return Boolean(hasValidTabs && hasValidKpis);
  } catch {
    // not valid JSON
  }
  return false;
}

/**
 * Safely parses a JSON string into an A2UIManifest structure.
 */
export function parseA2UIManifest(content?: string | null): A2UIManifest | null {
  if (!content) return null;
  try {
    const parsed = JSON.parse(content.trim());
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      if (
        parsed.schemaVersion ||
        Array.isArray(parsed.tabs) ||
        Array.isArray(parsed.kpis) ||
        parsed.title
      ) {
        return {
          schemaVersion: parsed.schemaVersion || "1.0",
          title: parsed.title || "A2UI Dashboard",
          asOfDate: parsed.asOfDate,
          subtitle: parsed.subtitle,
          kpis: Array.isArray(parsed.kpis) ? parsed.kpis : [],
          tabs: Array.isArray(parsed.tabs) ? parsed.tabs : [],
        };
      }
    }
  } catch (err) {
    console.error("Failed to parse A2UI manifest JSON:", err);
  }
  return null;
}

/**
 * Formats a currency value as standard USD/currency string.
 */
export function formatA2UIMoney(amount: number | string | undefined | null): string {
  if (amount === undefined || amount === null || amount === "") return "$0.00";
  const num = typeof amount === "number" ? amount : parseFloat(String(amount).replace(/,/g, ""));
  if (isNaN(num)) return "$0.00";
  return (
    "$" +
    num.toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    })
  );
}

/**
 * Formats a percentage value.
 */
export function formatA2UIPercent(val: number | string | undefined | null): string {
  if (val === undefined || val === null || val === "") return "0.00%";
  const num = typeof val === "number" ? val : parseFloat(String(val).replace(/,/g, ""));
  if (isNaN(num)) return String(val);
  return `${num > 0 ? "+" : ""}${num.toFixed(2)}%`;
}

/**
 * Formats a numeric value with thousands separators.
 */
export function formatA2UINumber(val: number | string | undefined | null): string {
  if (val === undefined || val === null || val === "") return "0";
  const num = typeof val === "number" ? val : parseFloat(String(val).replace(/,/g, ""));
  if (isNaN(num)) return String(val);
  return num.toLocaleString("en-US");
}

/**
 * Resolves a referenced asset path relative to the manifest file path.
 */
export function resolveAssetPath(assetPath: string, manifestPath?: string | null): string {
  if (!assetPath) return "";
  const cleanAsset = assetPath.trim();
  if (cleanAsset.startsWith("/")) return cleanAsset;
  if (!manifestPath) return cleanAsset;

  const manifestDir = manifestPath.includes("/")
    ? manifestPath.substring(0, manifestPath.lastIndexOf("/"))
    : "";

  if (!manifestDir) return cleanAsset;
  return `${manifestDir}/${cleanAsset}`;
}

/**
 * Asynchronously loads a workspace file text (CSV or Markdown) by trying relative and direct paths.
 * Reuses the centralized getFileContent client in lib/api.ts for unified error handling.
 */
export async function fetchWorkspaceAsset(
  sessionId: string,
  assetPath: string,
  manifestPath?: string | null,
): Promise<string | null> {
  if (!sessionId || !assetPath) return null;

  const candidatePaths: string[] = [];
  const resolved = resolveAssetPath(assetPath, manifestPath);
  if (resolved) candidatePaths.push(resolved);
  if (assetPath !== resolved) candidatePaths.push(assetPath);

  for (const p of candidatePaths) {
    try {
      const data = await getFileContent(sessionId, p);
      if (data && typeof data.content === "string") {
        return data.content;
      }
    } catch {
      // ignore and try next candidate
    }
  }

  return null;
}
