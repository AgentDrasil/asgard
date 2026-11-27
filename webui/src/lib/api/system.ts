import type {
  CompressionMetrics,
  SystemLogEntry,
  SystemLogsResponse,
  SystemStatusResponse,
} from "../../types";
import { apiFetch } from "./client";

// Fetch system diagnostics status (returns null on 404 or non-ok)
export async function getSystemStatus(): Promise<SystemStatusResponse | null> {
  try {
    const res = await apiFetch("/api/system/status");
    if (res.status === 404) return null;
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("getSystemStatus error:", err);
  }
  return null;
}

// Fetch diagnostic system logs (returns empty array on non-ok or network failure)
export async function getSystemLogs(level?: string): Promise<SystemLogEntry[]> {
  try {
    const url =
      level && level !== "all"
        ? `/api/system/logs?level=${encodeURIComponent(level)}`
        : "/api/system/logs";
    const res = await apiFetch(url);
    if (!res.ok) return [];
    const data: SystemLogsResponse = await res.json();
    return data.logs || [];
  } catch (err) {
    console.error("getSystemLogs error:", err);
    return [];
  }
}

// Clear the backend diagnostic logs and active error/warning snapshot (admin only).
export async function clearSystemLogs(): Promise<boolean> {
  try {
    const res = await apiFetch("/api/system/logs", { method: "DELETE" });
    return res.ok;
  } catch (err) {
    console.error("clearSystemLogs error:", err);
    return false;
  }
}

// Fetch command-output compression statistics (returns null on non-ok or network failure)
export async function getCompressionMetrics(): Promise<CompressionMetrics | null> {
  try {
    const res = await apiFetch("/api/compression-metrics");
    if (!res.ok) return null;
    return await res.json();
  } catch (err) {
    console.error("getCompressionMetrics error:", err);
    return null;
  }
}
