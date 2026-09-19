import { ref } from "vue";
import { getBackendConfig } from "../lib/api";

const proxyEnvs = ref<string[]>([]);
let inFlightPromise: Promise<string[]> | null = null;
let lastFetchTime = 0;
const CACHE_TTL_MS = 10_000; // 10s TTL for balanced responsiveness and proxy hot-reload

export function useProxyEnvs() {
  const load = async (): Promise<string[]> => {
    if (lastFetchTime > 0 && Date.now() - lastFetchTime < CACHE_TTL_MS) {
      return proxyEnvs.value;
    }

    if (inFlightPromise) return inFlightPromise;

    inFlightPromise = (async () => {
      try {
        const cfg = await getBackendConfig();
        if (cfg && Array.isArray(cfg.proxy_envs)) {
          proxyEnvs.value = cfg.proxy_envs;
        } else {
          proxyEnvs.value = [];
        }
      } catch (err) {
        console.warn("failed to fetch proxy envs:", err);
      } finally {
        // Record success and failure alike: a failing backend must not be
        // re-hit on every menu open, while the TTL still bounds staleness.
        lastFetchTime = Date.now();
        inFlightPromise = null;
      }
      return proxyEnvs.value;
    })();

    return inFlightPromise;
  };

  return {
    proxyEnvs,
    load,
  };
}

export function __resetProxyEnvsCacheForTest() {
  proxyEnvs.value = [];
  inFlightPromise = null;
  lastFetchTime = 0;
}
