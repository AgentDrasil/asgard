import type { SupportedOS } from "../types";

export function detectClientOS(): SupportedOS {
  if (typeof navigator === "undefined") {
    return "linux";
  }

  // 1. Modern User-Agent Client Hints (UA-CH) platform
  const navAny = navigator as any;
  if (navAny.userAgentData && typeof navAny.userAgentData.platform === "string") {
    const platform = navAny.userAgentData.platform.toLowerCase();
    if (platform.includes("mac")) return "mac";
    if (platform.includes("win")) return "windows";
    if (platform.includes("linux") || platform.includes("android")) return "linux";
  }

  // 2. Fallback to navigator.platform
  const platform = (navigator.platform || "").toLowerCase();
  if (
    platform.includes("mac") ||
    platform.includes("iphone") ||
    platform.includes("ipad") ||
    platform.includes("ipod")
  ) {
    return "mac";
  }
  if (platform.includes("win")) {
    return "windows";
  }
  if (platform.includes("linux")) {
    return "linux";
  }

  // 3. Fallback to navigator.userAgent
  const userAgent = (navigator.userAgent || "").toLowerCase();
  if (userAgent.includes("macintosh") || userAgent.includes("mac os x")) {
    return "mac";
  }
  if (userAgent.includes("windows")) {
    return "windows";
  }
  if (userAgent.includes("linux")) {
    return "linux";
  }

  return "linux";
}

export function isMacOS(): boolean {
  return detectClientOS() === "mac";
}

export function isWindows(): boolean {
  return detectClientOS() === "windows";
}

export function isLinux(): boolean {
  return detectClientOS() === "linux";
}

export const OS_DISPLAY_NAMES: Record<SupportedOS, string> = {
  mac: "macOS",
  windows: "Windows",
  linux: "Linux",
};
