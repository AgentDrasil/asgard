import { ref, watch } from "vue";
import type { AgentInfo } from "../types";
import { getAgents } from "../lib/api";

export function useAgents() {
  const agents = ref<AgentInfo[]>([]);
  const selectedAgentId = ref("");
  const selectedDir = ref("");
  const selectedModel = ref("");

  const loadAgents = async () => {
    const loadedAgents = await getAgents();
    agents.value = loadedAgents;

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
