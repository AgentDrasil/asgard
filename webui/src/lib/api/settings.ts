import type { KeybindingsApiResponse, KeybindingsOverrides } from "../../types";
import { apiFetch } from "./client";

export interface UserProxyKeyRule {
  host: string;
  path_prefix: string;
  header_key: string;
}

export interface UserProxyKeyMeta extends Partial<UserProxyKeyRule> {
  key_name: string;
  updated_at: string;
}

export async function getUserProxyKeys(): Promise<UserProxyKeyMeta[]> {
  const res = await apiFetch("/api/user/proxy-keys");
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data?.error || `Failed to fetch user proxy keys: ${res.statusText}`);
  }
  return res.json();
}

export async function saveUserProxyKey(
  key_name: string,
  value: string,
  rule?: Partial<UserProxyKeyRule>,
): Promise<void> {
  const res = await apiFetch("/api/user/proxy-keys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ key_name, value, ...rule }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data?.error || `Failed to save user proxy key: ${res.statusText}`);
  }
}

export async function deleteUserProxyKey(key_name: string): Promise<void> {
  const res = await apiFetch(`/api/user/proxy-keys/${encodeURIComponent(key_name)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data?.error || `Failed to delete user proxy key: ${res.statusText}`);
  }
}

// Fetch keybindings from /api/keybindings (public GET)
export async function getKeybindings(): Promise<KeybindingsApiResponse | null> {
  try {
    const res = await apiFetch("/api/keybindings");
    if (res.status === 200) {
      return await res.json();
    }
    if (res.status === 500) {
      const body = await res.json().catch(() => null);
      return {
        overrides: {},
        error: body?.error || "Failed to load keybindings (corrupted file)",
      };
    }
    const body = await res.json().catch(() => null);
    return {
      overrides: {},
      error: body?.error || `Failed to fetch keybindings (${res.status})`,
    };
  } catch (err: any) {
    console.error("getKeybindings error:", err);
    return null;
  }
}

// Save keybindings to /api/manage/keybindings (protected PUT)
export async function saveKeybindings(
  overrides: KeybindingsOverrides,
): Promise<{ success: boolean; error?: string }> {
  try {
    const res = await apiFetch("/api/manage/keybindings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ overrides }),
    });
    const body = await res.json().catch(() => null);
    if (res.ok) {
      return { success: true };
    }
    return {
      success: false,
      error: body?.error || `Failed to save keybindings (${res.status})`,
    };
  } catch (err: any) {
    console.error("saveKeybindings error:", err);
    return {
      success: false,
      error: err?.message || "Failed to save keybindings",
    };
  }
}
