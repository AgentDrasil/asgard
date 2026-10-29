import { computed, ref } from "vue";
import type { ChatSession, RunnerInfo } from "../types";
import { getRunners } from "../lib/api";

export const SELECTED_RUNNER_KEY = "asgard_selected_runner";
export const RUNNER_POLL_INTERVAL_MS = 15000;

// Module-level state: the sidebar, App, settings and quota modal all observe
// the same runner list and selection.
const runners = ref<RunnerInfo[]>([]);
const backendVersion = ref("");
const loading = ref(false);
const preferredRunnerId = ref(readStoredRunner());

let pollTimer: ReturnType<typeof setInterval> | null = null;
let pollUsers = 0;

function readStoredRunner(): string {
  try {
    return localStorage.getItem(SELECTED_RUNNER_KEY) || "";
  } catch {
    return "";
  }
}

function writeStoredRunner(id: string) {
  try {
    localStorage.setItem(SELECTED_RUNNER_KEY, id);
  } catch {
    // storage unavailable: the selection just lives in memory
  }
}

// Effective selection: the stored choice if that runner is online, else
// the first online runner, else the first runner. Empty in legacy mode.
const selectedRunnerId = computed(() => {
  const list = runners.value;
  if (list.length === 0) return "";
  const preferred = list.find((r) => r.node_id === preferredRunnerId.value);
  if (preferred?.online) return preferred.node_id;
  return (list.find((r) => r.online) || list[0]).node_id;
});

// Runner mode is on when the backend reports at least one runner.
const runnerMode = computed(() => runners.value.length > 0);

const selectedRunner = computed(
  () => runners.value.find((r) => r.node_id === selectedRunnerId.value) || null,
);

async function refreshRunners() {
  loading.value = true;
  try {
    const res = await getRunners();
    if (res) {
      runners.value = res.runners;
      backendVersion.value = res.backend_version;
    }
  } catch (err) {
    console.error("refreshRunners error:", err);
  } finally {
    loading.value = false;
  }
}

function selectRunner(id: string) {
  preferredRunnerId.value = id;
  writeStoredRunner(id);
}

// The runner a request should target: the chat's bound runner for an existing
// chat (never the dropdown), otherwise the selected one.
function runnerForSession(session?: Pick<ChatSession, "runnerId"> | null): string {
  return session?.runnerId || selectedRunnerId.value;
}

function startPolling() {
  pollUsers++;
  if (pollTimer) return;
  void refreshRunners();
  pollTimer = setInterval(() => void refreshRunners(), RUNNER_POLL_INTERVAL_MS);
}

function stopPolling() {
  pollUsers = Math.max(0, pollUsers - 1);
  if (pollUsers === 0 && pollTimer) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

// Test helper: reset module state.
export function resetRunnersForTest() {
  runners.value = [];
  backendVersion.value = "";
  preferredRunnerId.value = readStoredRunner();
  pollUsers = 0;
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = null;
}

export function useRunners() {
  return {
    runners,
    backendVersion,
    loading,
    runnerMode,
    selectedRunnerId,
    selectedRunner,
    refreshRunners,
    selectRunner,
    runnerForSession,
    startPolling,
    stopPolling,
  };
}
