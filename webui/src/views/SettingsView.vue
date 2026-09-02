<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import ThemeSelector from "../components/sidebar/ThemeSelector.vue";
import LanguageSelector from "../components/sidebar/LanguageSelector.vue";
import QuotaModal from "../components/sidebar/QuotaModal.vue";
import { reloadAgents, getSystemLogs } from "../lib/api";
import { useToast } from "../composables/useToast";
import { useRestartFlow } from "../composables/useRestartFlow";

const router = useRouter();
const { t } = useI18n();
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
      toast.success(t("settings.reloadSuccessMessage"), {
        title: t("settings.reloadSuccessTitle"),
      });
    } else {
      toast.error(result.error || t("settings.reloadErrorMessage"), {
        title: t("settings.reloadErrorTitle"),
      });
    }
  } catch (err: any) {
    toast.error(err?.message || t("settings.reloadErrorMessage"), {
      title: t("settings.reloadErrorTitle"),
    });
  } finally {
    isReloading.value = false;
  }
};

const navigateToConfig = () => {
  router.push("/settings/config");
};

const navigateToKeybindings = () => {
  router.push("/settings/keybindings");
};

const navigateToLogs = () => {
  router.push("/settings/logs");
};

const navigateBack = () => {
  if (window.history.state?.back) {
    router.back();
  } else {
    router.push("/newchat");
  }
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
          :title="t('settings.backToChat')"
          :aria-label="t('settings.backToChat')"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2">
          <Icon icon="mynaui:cog" class="w-5 h-5 text-primary" />
          <h1 class="text-base font-semibold md:text-lg">{{ t("settings.title") }}</h1>
        </div>
      </div>
    </header>

    <!-- Main Content -->
    <div class="p-4 md:p-6 max-w-4xl w-full mx-auto space-y-6 flex-1">
      <!-- Section: Preferences -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.preferences") }}
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-3 md:gap-4">
          <!-- Language Setting Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex items-center justify-between gap-4"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="material-symbols:translate" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.language") }}</span>
              </div>
              <p class="text-xs text-base-content/70">
                {{ t("settings.languageDesc") }}
              </p>
            </div>
            <div class="w-36 shrink-0">
              <LanguageSelector />
            </div>
          </div>

          <!-- Theme Setting Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex items-center justify-between gap-4"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mdi:paint-outline" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.theme") }}</span>
              </div>
              <p class="text-xs text-base-content/70">
                {{ t("settings.themeDesc") }}
              </p>
            </div>
            <ThemeSelector />
          </div>
        </div>
      </section>

      <!-- Section: System & Server Actions -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.systemActions") }}
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-3 md:gap-4">
          <!-- Reload Agents Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex flex-col justify-between gap-3"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mynaui:refresh" class="w-5 h-5 text-info" />
                <span>{{ t("settings.reloadAgents") }}</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                {{ t("settings.reloadAgentsDesc") }}
              </p>
            </div>
            <button
              @click="handleReloadAgents"
              class="btn btn-outline btn-sm w-full gap-2 mt-2"
              :disabled="isReloading || isRestarting"
            >
              <Icon icon="mynaui:refresh" :class="['w-4 h-4', { 'animate-spin': isReloading }]" />
              <span>{{ isReloading ? t("settings.reloading") : t("settings.reloadAgents") }}</span>
            </button>
          </div>

          <!-- Restart Server Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex flex-col justify-between gap-3"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mynaui:power" class="w-5 h-5 text-error" />
                <span>{{ t("settings.restartServer") }}</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                {{ t("settings.restartServerDesc") }}
              </p>
            </div>
            <button
              @click="openRestartConfirm"
              class="btn btn-error btn-outline btn-sm w-full gap-2 mt-2"
              :disabled="isRestarting"
            >
              <Icon icon="mynaui:power" :class="['w-4 h-4', { 'animate-spin': isRestarting }]" />
              <span>{{
                isRestarting ? t("settings.restarting") : t("settings.restartServer")
              }}</span>
            </button>
          </div>

          <!-- Quota Card -->
          <div
            class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex flex-col justify-between gap-3"
          >
            <div class="space-y-1">
              <div class="font-medium text-base-content flex items-center gap-2">
                <Icon icon="mynaui:chart-bar-one" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.usageAndQuota") }}</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                {{ t("settings.usageAndQuotaDesc") }}
              </p>
            </div>
            <button
              @click="isQuotaModalOpen = true"
              class="btn btn-outline btn-sm w-full gap-2 mt-2"
            >
              <Icon icon="mynaui:chart-bar-one" class="w-4 h-4" />
              <span>{{ t("settings.checkQuota") }}</span>
            </button>
          </div>
        </div>
      </section>

      <!-- Section: Navigation & Tools -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.configAndDiagnostics") }}
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
                <span>{{ t("settings.editConfig") }}</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                {{ t("settings.editConfigDesc") }}
              </p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 group-hover:text-primary group-hover:translate-x-1 transition-all shrink-0"
            />
          </div>

          <!-- Keyboard Shortcuts Card -->
          <div
            role="button"
            tabindex="0"
            @click="navigateToKeybindings"
            @keydown.enter.space.prevent="navigateToKeybindings"
            class="group rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 flex items-center justify-between gap-4 cursor-pointer hover:border-primary/50 hover:bg-base-200 transition-all shadow-xs focus:outline-hidden focus:ring-2 focus:ring-primary/50"
          >
            <div class="space-y-1">
              <div
                class="font-medium text-base-content flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="material-symbols:keyboard-outline" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.keyboardShortcuts") }}</span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                {{ t("settings.keyboardShortcutsDesc") }}
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
                <span>{{ t("settings.logsAndDiagnostics") }}</span>
                <span v-if="totalErrors > 0" class="badge badge-error badge-xs font-bold">
                  {{ t("settings.errorsCount", { count: totalErrors }) }}
                </span>
                <span v-else-if="totalWarnings > 0" class="badge badge-warning badge-xs font-bold">
                  {{ t("settings.warnsCount", { count: totalWarnings }) }}
                </span>
              </div>
              <p class="text-xs text-base-content/70 leading-relaxed">
                {{ t("settings.logsAndDiagnosticsDesc") }}
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
              <h3 class="font-bold text-lg text-base-content">
                {{ t("app.restartConfirmTitle") }}
              </h3>
              <p class="text-sm text-base-content/70 leading-relaxed">
                {{ t("app.restartConfirmDesc") }}
              </p>
            </div>
          </div>

          <div
            class="bg-base-300/60 rounded-xl p-3.5 border border-base-100/40 text-xs text-base-content/80 space-y-1.5"
          >
            <div class="font-semibold text-warning flex items-center gap-1.5">
              <Icon icon="mynaui:info-triangle" class="h-4 w-4 shrink-0" />
              <span>{{ t("app.prerequisites") }}</span>
            </div>
            <p>
              {{ t("app.prerequisitesDesc") }}
            </p>
            <p class="text-base-content/60 text-[11px]">
              {{ t("app.restartNote") }}
            </p>
          </div>

          <div class="flex items-center justify-end gap-2 pt-2">
            <button
              @click="closeRestartConfirm"
              class="btn btn-ghost btn-sm"
              :disabled="isRestarting"
            >
              {{ t("common.cancel") }}
            </button>
            <button
              @click="triggerRestartWorkflow"
              class="btn btn-error btn-sm gap-1.5"
              :disabled="isRestarting"
            >
              <Icon icon="mynaui:power" class="h-4 w-4" />
              <span>{{ t("app.confirmRestart") }}</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>
