import { ref } from "vue";
import { getBackendConfig } from "../lib/api";

const proxyEnvs = ref<string[]>([]);
let loaded = false;
let loadPromise: Promise<string[]> | null = null;

export function useProxyEnvs() {
  const load = async (): Promise<string[]> => {
    if (loaded) return proxyEnvs.value;
    if (loadPromise) return loadPromise;

    loadPromise = (async () => {
      try {
        const cfg = await getBackendConfig();
        if (cfg && Array.isArray(cfg.proxy_envs)) {
          proxyEnvs.value = cfg.proxy_envs;
        }
      } catch (err) {
        console.warn("failed to load proxy envs:", err);
      } finally {
        loaded = true;
        loadPromise = null;
      }
      return proxyEnvs.value;
    })();

    return loadPromise;
  };

  return {
    proxyEnvs,
    load,
  };
}
