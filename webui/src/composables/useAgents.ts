import { ref, watch } from "vue";
import type { AgentInfo } from "../types";
import { getAgents } from "../lib/api";
import { useRunners } from "./useRunners";

export function useAgents() {
  const agents = ref<AgentInfo[]>([]);
  const selectedAgentId = ref("");
  const selectedDir = ref("");
  const selectedModel = ref("");

  const { selectedRunnerId } = useRunners();

  // A newer load supersedes older ones (out-of-order responses for different
  // runners must not leave the list scoped to the wrong one), and concurrent
  // loads for the same runner share one request.
  let generation = 0;
  let inflight: { key: string; promise: Promise<void> } | null = null;

  const applyAgents = (loadedAgents: AgentInfo[]) => {
    agents.value = loadedAgents;

    // Keep the current selection across a reload (e.g. the runner changed)
    // when that agent is still available.
    if (selectedAgentId.value && loadedAgents.some((a) => a.id === selectedAgentId.value)) {
      return;
    }

    const mainAgents = loadedAgents.filter((a) => a.main_agent !== false);
    const initialAgent = mainAgents.length > 0 ? mainAgents[0] : loadedAgents[0];
    if (initialAgent) {
      selectedAgentId.value = initialAgent.id;
      if (initialAgent.run_dirs.length > 0) {
        selectedDir.value = initialAgent.run_dirs[0];
      }
      selectedModel.value = "";
    }
  };

  const loadAgents = (runner?: string): Promise<void> => {
    // Agents are scoped to a runner (global plus the runner's own).
    const key = runner || selectedRunnerId.value || "";
    if (inflight && inflight.key === key) return inflight.promise;
    const myGeneration = ++generation;
    const promise = (async () => {
      const loadedAgents = await getAgents(key || undefined);
      if (myGeneration !== generation) return; // superseded by a newer load
      applyAgents(loadedAgents);
    })().finally(() => {
      if (inflight?.promise === promise) inflight = null;
    });
    inflight = { key, promise };
    return promise;
  };

  // Update selected workspace directory & model when active agent changes.
  // flush: 'sync' so derived state updates immediately; otherwise an explicit
  // `selectedDir` assignment right after changing the agent (e.g. handleNewChat
  // from the sidebar "+") would be clobbered by this watcher on the next tick.
  watch(
    selectedAgentId,
    (newAgentId) => {
      const currentAgent = agents.value.find((a) => a.id === newAgentId);
      if (currentAgent && currentAgent.run_dirs.length > 0) {
        selectedDir.value = currentAgent.run_dirs[0];
      } else {
        selectedDir.value = "";
      }
      selectedModel.value = "";
    },
    { flush: "sync" },
  );

  return {
    agents,
    selectedAgentId,
    selectedDir,
    selectedModel,
    loadAgents,
  };
}
