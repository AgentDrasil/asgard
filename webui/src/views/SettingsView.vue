<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useRouter } from "vue-router";
import { Icon } from "@iconify/vue";
import ThemeSelector from "../components/sidebar/ThemeSelector.vue";
import QuotaModal from "../components/sidebar/QuotaModal.vue";
import { reloadAgents, getSystemLogs } from "../lib/api";
import { useToast } from "../composables/useToast";
import { useRestartFlow } from "../composables/useRestartFlow";

const router = useRouter();
const toast = useToast();
const { toastHistory } = toast;

const {
  isRestarting,
  isRestartConfirmOpen,
  openRestartConfirm,
  closeRestartConfirm,
  triggerRestartWorkflow,
} = useRestartFlow();

const isReloading = ref(false);
const isQuotaModalOpen = ref(false);
const backendErrorCount = ref(0);
const backendWarnCount = ref(0);

const fetchBackendLogCounts = async () => {
  try {
    const logs = await getSystemLogs();
    backendErrorCount.value = logs.filter((l) => l.level === "error").length;
    backendWarnCount.value = logs.filter((l) => l.level === "warn").length;
  } catch (err) {
    console.error("Failed to fetch backend log counts:", err);
  }
};

onMounted(() => {
  void fetchBackendLogCounts();
});

const toastErrorCount = computed(() => toastHistory.value.filter((t) => t.type === "error").length);
const toastWarnCount = computed(
  () => toastHistory.value.filter((t) => t.type === "warning").length,
);

const totalErrors = computed(() => backendErrorCount.value + toastErrorCount.value);
const totalWarnings = computed(() => backendWarnCount.value + toastWarnCount.value);

const handleReloadAgents = async () => {
  if (isReloading.value) return;
  isReloading.value = true;
  try {
    const result = await reloadAgents();
    if (result.success) {
      toast.success("Agent configuration reloaded successfully", {
        title: "Reload Success",
      });
    } else {
      toast.error(result.error || "Failed to reload agent configuration", {
        title: "Reload Error",
      });
    }
  } catch (err: any) {
    toast.error(err?.message || "Failed to reload agent configuration", {
      title: "Reload Error",
    });
  } finally {
    isReloading.value = false;
  }
};

const navigateToConfig = () => {
  router.push("/settings/config");
};

const navigateToLogs = () => {
  router.push("/settings/logs");
};

const navigateBack = () => {
  router.push("/newchat");
};
</script>

<template>
  <div class="flex flex-col h-full w-full bg-base-100 overflow-y-auto">
    <!-- Header -->
    <header
      class="sticky top-0 z-20 flex items-center justify-between border-b border-base-300 bg-base-100/90 px-4 py-3 backdrop-blur md:px-6"
    >
      <div class="flex items-center gap-3">
        <button
          @click="navigateBack"
          class="btn btn-ghost btn-sm btn-square"
          title="Back to Chat"
          aria-label="Back to Chat"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2">
          <Icon icon="mynaui:cog" class="w-5 h-5 text-primary" />
          <h1 class="text-base font-semibold md:text-lg">Settings</h1>
        </div>
      </div>
    </header>

    <!-- Main Content -->
    <div class="p-4 md:p-6 max-w-4xl w-full mx-auto space-y-6 flex-1">
      <!-- Section: Appearance -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          Appearance
        </h2>
        <div
          class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex items-center justify-between gap-4"
        >
          <div class="space-y-1">
            <div class="font-medium text-base-content flex items-center gap-2">
              <Icon icon="mdi:paint-outline" class="w-5 h-5 text-primary" />
              <span>Theme</span>
            </div>
            <p class="text-xs text-base-content/70">
              Customize the look and feel of the interface (DaisyUI & Catppuccin themes).
            </p>
          </div>
          <ThemeSelector />
        </div>
      </section>

      <!-- Section: System & Server Actions -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          System & Server Actions
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-3 md:gap-4">
          <!-- Reload Agents Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex flex-col justify-between gap-3"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mynaui:refresh" class="w-5 h-5 text-info" />
                <span>Reload Agents</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                Hot-reload agent configurations and workspaces without stopping the server.
              </p>
            </div>
            <button
              @click="handleReloadAgents"
              class="btn btn-outline btn-sm w-full gap-2 mt-2"
              :disabled="isReloading || isRestarting"
            >
              <Icon icon="mynaui:refresh" :class="['w-4 h-4', { 'animate-spin': isReloading }]" />
              <span>{{ isReloading ? "Reloading..." : "Reload Agents" }}</span>
            </button>
          </div>

          <!-- Restart Server Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex flex-col justify-between gap-3"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mynaui:power" class="w-5 h-5 text-error" />
                <span>Restart Server</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                Gracefully terminate and restart the backend service (requires auto-restart
                container).
              </p>
            </div>
            <button
              @click="openRestartConfirm"
              class="btn btn-error btn-outline btn-sm w-full gap-2 mt-2"
              :disabled="isRestarting"
            >
              <Icon icon="mynaui:power" :class="['w-4 h-4', { 'animate-spin': isRestarting }]" />
              <span>{{ isRestarting ? "Restarting..." : "Restart Server" }}</span>
            </button>
          </div>

          <!-- Quota Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex flex-col justify-between gap-3"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mynaui:chart-bar-one" class="w-5 h-5 text-primary" />
                <span>Usage & Quota</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                View current token usage, credit balance, and resource consumption details.
              </p>
            </div>
            <button
              @click="isQuotaModalOpen = true"
              class="btn btn-outline btn-sm w-full gap-2 mt-2"
            >
              <Icon icon="mynaui:chart-bar-one" class="w-4 h-4" />
              <span>Check Quota</span>
            </button>
          </div>
        </div>
      </section>

      <!-- Section: Navigation & Tools -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          Configuration & Diagnostics
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-3 md:gap-4">
          <!-- Edit Config Card -->
          <div
            role="button"
            tabindex="0"
            @click="navigateToConfig"
            @keydown.enter.space.prevent="navigateToConfig"
            class="group rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex items-center justify-between gap-4 cursor-pointer hover:border-primary/50 hover:bg-base-200 transition-all shadow-xs focus:outline-hidden focus:ring-2 focus:ring-primary/50"
          >
            <div class="space-y-1">
              <div
                class="font-medium text-base-content flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="mynaui:cog" class="w-5 h-5 text-primary" />
                <span>Edit Configuration</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                Open full-page YAML configuration editor with validation and save & restart
                capability.
              </p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 group-hover:text-primary group-hover:translate-x-1 transition-all shrink-0"
            />
          </div>

          <!-- Logs & Diagnostics Card -->
          <div
            role="button"
            tabindex="0"
            @click="navigateToLogs"
            @keydown.enter.space.prevent="navigateToLogs"
            class="group rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex items-center justify-between gap-4 cursor-pointer hover:border-primary/50 hover:bg-base-200 transition-all shadow-xs focus:outline-hidden focus:ring-2 focus:ring-primary/50"
          >
            <div class="space-y-1">
              <div
                class="font-medium text-base-content flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="material-symbols:receipt-long-outline" class="w-5 h-5 text-primary" />
                <span>System Logs & Diagnostics</span>
                <span v-if="totalErrors > 0" class="badge badge-error badge-xs font-bold">
                  {{ totalErrors }} error{{ totalErrors > 1 ? "s" : "" }}
                </span>
                <span v-else-if="totalWarnings > 0" class="badge badge-warning badge-xs font-bold">
                  {{ totalWarnings }} warn
                </span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                View system startup diagnostics, error history, and active warnings.
              </p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 group-hover:text-primary group-hover:translate-x-1 transition-all shrink-0"
            />
          </div>
        </div>
      </section>
    </div>

    <!-- Quota Modal -->
    <QuotaModal v-model="isQuotaModalOpen" />

    <!-- Restart Confirmation Modal -->
    <Transition name="fade">
      <div
        v-if="isRestartConfirmOpen"
        class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-center justify-center p-4"
        @click.self="closeRestartConfirm"
      >
        <div
          class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-md p-6 shadow-2xl space-y-4"
        >
          <div class="flex items-start gap-3">
            <div class="p-2.5 rounded-full bg-warning/10 text-warning shrink-0">
              <Icon icon="mynaui:danger" class="h-6 w-6" />
            </div>
            <div class="space-y-1">
              <h3 class="font-bold text-lg text-base-content">Confirm Server Restart?</h3>
              <p class="text-sm text-base-content/70 leading-relaxed">
                This will gracefully terminate the current Asgard backend process.
              </p>
            </div>
          </div>

          <div
            class="bg-base-300/60 rounded-xl p-3.5 border border-base-100/40 text-xs text-base-content/80 space-y-1.5"
          >
            <div class="font-semibold text-warning flex items-center gap-1.5">
              <Icon icon="mynaui:info-triangle" class="h-4 w-4 shrink-0" />
              <span>Prerequisites</span>
            </div>
            <p>
              Please ensure your Docker container is configured with an automatic restart policy
              (such as <code>--restart=always</code> or <code>--restart=unless-stopped</code>),
              otherwise the container will not restart after the process exits.
            </p>
            <p class="text-base-content/60 text-[11px]">
              The page will poll system status and automatically refresh once the server is back
              online.
            </p>
          </div>

          <div class="flex items-center justify-end gap-2 pt-2">
            <button
              @click="closeRestartConfirm"
              class="btn btn-ghost btn-sm"
              :disabled="isRestarting"
            >
              Cancel
            </button>
            <button
              @click="triggerRestartWorkflow"
              class="btn btn-error btn-sm gap-1.5"
              :disabled="isRestarting"
            >
              <Icon icon="mynaui:power" class="h-4 w-4" />
              <span>Confirm Restart</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>
