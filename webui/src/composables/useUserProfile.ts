import { ref } from "vue";
import { apiFetch, isAuthEnabled } from "../lib/api";

export interface UserProfile {
  subject: string;
  name: string;
  email?: string;
  avatarUrl?: string;
  isAdmin?: boolean;
}

// Shared across components so the profile is fetched once per page load.
const profile = ref<UserProfile | null>(null);
let inFlightPromise: Promise<UserProfile | null> | null = null;
let loaded = false;

export function useUserProfile() {
  // load resolves to null when auth is disabled or the profile is unavailable,
  // in which case callers fall back to the generic "You" label.
  const load = async (): Promise<UserProfile | null> => {
    if (loaded) return profile.value;
    if (inFlightPromise) return inFlightPromise;

    inFlightPromise = (async () => {
      try {
        if (!isAuthEnabled()) return profile.value;
        const res = await apiFetch("/api/auth/me");
        if (res.ok) {
          profile.value = (await res.json()) as UserProfile;
          loaded = true;
        }
      } catch (err) {
        console.warn("failed to fetch user profile:", err);
      } finally {
        inFlightPromise = null;
      }
      return profile.value;
    })();

    return inFlightPromise;
  };

  return { profile, load };
}

export function __resetUserProfileForTest() {
  profile.value = null;
  inFlightPromise = null;
  loaded = false;
}
