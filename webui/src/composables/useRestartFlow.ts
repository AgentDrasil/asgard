import { ref } from "vue";
import { restartServer, getSystemStatus } from "../lib/api";
import { useToast } from "./useToast";

let restartAbortController: AbortController | null = null;

export function useRestartFlow() {
  const isRestarting = ref(false);
  const isRestartConfirmOpen = ref(false);
  const toast = useToast();

  const openRestartConfirm = () => {
    if (isRestarting.value) return;
    isRestartConfirmOpen.value = true;
  };

  const closeRestartConfirm = () => {
    if (isRestarting.value) return;
    isRestartConfirmOpen.value = false;
  };

  const triggerRestartWorkflow = async () => {
    isRestartConfirmOpen.value = false;
    if (isRestarting.value) return;
    isRestarting.value = true;

    // 1. Send restart signal
    const accepted = await restartServer();
    if (!accepted) {
      isRestarting.value = false;
      toast.error("Restart request rejected by server (HTTP error). Please check backend logs.", {
        title: "Restart Failed",
      });
      return;
    }

    // 2. Poll /api/system/status with backoff and timeout (120s)
    toast.info("Server is restarting, page will refresh automatically once ready...", {
      title: "Restarting",
      duration: 10000,
    });

    if (restartAbortController) {
      restartAbortController.abort();
    }
    restartAbortController = new AbortController();
    const abortSignal = restartAbortController.signal;
    const startTime = Date.now();
    const timeoutMs = 120_000;
    const initialDelay = 1000;
    const interval = 1500;

    // Initial delay to give process time to exit
    await new Promise((resolve) => setTimeout(resolve, initialDelay));

    const pollStatus = async () => {
      while (Date.now() - startTime < timeoutMs) {
        if (abortSignal.aborted) return;
        const status = await getSystemStatus();
        if (status !== null) {
          if (abortSignal.aborted) return;
          // Server is back online!
          toast.success("Server is back online, refreshing page...", { title: "Restart Complete" });
          setTimeout(() => {
            if (!abortSignal.aborted) {
              window.location.reload();
            }
          }, 500);
          return;
        }
        await new Promise((resolve) => setTimeout(resolve, interval));
      }

      if (abortSignal.aborted) return;

      // Timeout reached
      isRestarting.value = false;
      toast.error(
        "Server restart probe timed out (120s). If the container is not configured with a restart policy (e.g. --restart=always), check Docker container status manually (docker ps / docker logs).",
        { title: "Restart Timeout", duration: 0 },
      );
    };

    void pollStatus();
  };

  return {
    isRestarting,
    isRestartConfirmOpen,
    openRestartConfirm,
    closeRestartConfirm,
    triggerRestartWorkflow,
  };
}
