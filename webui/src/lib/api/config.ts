import type { FirebaseWebpushWebConfig, VoiceTokenResponse } from "../../types";
import { apiFetch } from "./client";

export async function registerPushToken(token: string): Promise<boolean> {
  try {
    const res = await apiFetch("/api/push/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    });
    return res.ok;
  } catch (err) {
    console.error("registerPushToken error:", err);
    return false;
  }
}

export async function getBackendConfig(): Promise<{
  firebase_webpush_web?: FirebaseWebpushWebConfig;
  default_ui_lang?: string;
  proxy_envs?: string[];
}> {
  try {
    const res = await apiFetch("/api/config");
    if (res.ok) return await res.json();
  } catch (err) {
    console.error("getBackendConfig error:", err);
  }
  return {};
}

export async function getVoiceToken(): Promise<VoiceTokenResponse> {
  const res = await apiFetch("/api/voice/token", { method: "POST" });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error || `Failed to fetch voice token: ${res.status}`);
  }
  return await res.json();
}
