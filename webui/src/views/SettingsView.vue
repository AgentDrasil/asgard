<script setup lang="ts">
import { ref, computed, watch, onMounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import ThemeSelector from "../components/sidebar/ThemeSelector.vue";
import LanguageSelector from "../components/sidebar/LanguageSelector.vue";
import { useUserProfile } from "../composables/useUserProfile";
import QuotaModal from "../components/sidebar/QuotaModal.vue";
import { getSystemLogs, isAuthEnabled, logout } from "../lib/api";
import { useToast } from "../composables/useToast";

const router = useRouter();
const { t } = useI18n();
const toast = useToast();
const { toastHistory } = toast;

const isQuotaModalOpen = ref(false);
const backendErrorCount = ref(0);
const backendWarnCount = ref(0);

// Authentication is opt-in: the section only appears when the backend runs
// OIDC, so a logout button never shows up in an unauthenticated deployment.
const authEnabled = isAuthEnabled();
const { profile, load: loadProfile } = useUserProfile();
const isAdmin = computed(() => !authEnabled || Boolean(profile.value?.isAdmin));

onMounted(() => void loadProfile());
const handleLogout = () => logout();

const fetchBackendLogCounts = async () => {
  if (!isAdmin.value) {
    backendErrorCount.value = 0;
    backendWarnCount.value = 0;
    return;
  }
  try {
    const logs = await getSystemLogs();
    backendErrorCount.value = logs.filter((l) => l.level === "error").length;
    backendWarnCount.value = logs.filter((l) => l.level === "warn").length;
  } catch (err) {
    console.error("Failed to fetch backend log counts:", err);
  }
};

watch(isAdmin, (admin) => {
  if (admin) {
    void fetchBackendLogCounts();
  } else {
    backendErrorCount.value = 0;
    backendWarnCount.value = 0;
  }
});

onMounted(() => {
  if (isAdmin.value) {
    void fetchBackendLogCounts();
  }
});

const toastErrorCount = computed(() => toastHistory.value.filter((t) => t.type === "error").length);
const toastWarnCount = computed(
  () => toastHistory.value.filter((t) => t.type === "warning").length,
);

const totalErrors = computed(
  () => (isAdmin.value ? backendErrorCount.value : 0) + toastErrorCount.value,
);
const totalWarnings = computed(
  () => (isAdmin.value ? backendWarnCount.value : 0) + toastWarnCount.value,
);

const go = (path: string) => router.push(path);

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

    <div class="p-4 md:p-6 max-w-2xl w-full mx-auto space-y-8 flex-1">
      <!-- Group: Preferences -->
      <section class="space-y-2">
        <h2 class="px-1 text-xs font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.preferences") }}
        </h2>
        <div class="rounded-xl border border-base-300 bg-base-200/50 divide-y divide-base-300">
          <div class="flex items-center justify-between gap-4 p-4">
            <div class="min-w-0">
              <div class="font-medium flex items-center gap-2">
                <Icon icon="material-symbols:translate" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.language") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">{{ t("settings.languageDesc") }}</p>
            </div>
            <div class="w-44 shrink-0"><LanguageSelector /></div>
          </div>
          <div class="flex items-center justify-between gap-4 p-4">
            <div class="min-w-0">
              <div class="font-medium flex items-center gap-2">
                <Icon icon="mdi:paint-outline" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.theme") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">{{ t("settings.themeDesc") }}</p>
            </div>
            <div class="w-56 shrink-0"><ThemeSelector /></div>
          </div>
          <div
            role="button"
            tabindex="0"
            @click="go('/settings/keybindings')"
            @keydown.enter.space.prevent="go('/settings/keybindings')"
            class="group flex items-center justify-between gap-4 p-4 cursor-pointer hover:bg-base-200 transition-colors focus:outline-hidden focus:ring-2 focus:ring-primary/50 rounded-b-xl"
          >
            <div class="min-w-0">
              <div
                class="font-medium flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="material-symbols:keyboard-outline" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.keyboardShortcuts") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">
                {{ t("settings.keyboardShortcutsDesc") }}
              </p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 shrink-0"
            />
          </div>
        </div>
      </section>

      <!-- Group: Account & Access -->
      <section class="space-y-2">
        <h2 class="px-1 text-xs font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.accountAndAccess") }}
        </h2>
        <div class="rounded-xl border border-base-300 bg-base-200/50 divide-y divide-base-300">
          <!-- Only when the backend runs OIDC -->
          <div v-if="authEnabled" class="flex items-center justify-between gap-4 p-4">
            <div class="min-w-0">
              <div class="font-medium flex items-center gap-2">
                <Icon icon="mynaui:shield-check" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.authSignedIn") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">{{ t("settings.authSignedInDesc") }}</p>
            </div>
            <button class="btn btn-outline btn-sm gap-1.5 shrink-0" @click="handleLogout">
              <Icon icon="mynaui:logout" class="h-4 w-4" />
              <span>{{ t("settings.signOut") }}</span>
            </button>
          </div>
          <div
            role="button"
            tabindex="0"
            @click="go('/settings/keys')"
            @keydown.enter.space.prevent="go('/settings/keys')"
            class="group flex items-center justify-between gap-4 p-4 cursor-pointer hover:bg-base-200 transition-colors focus:outline-hidden focus:ring-2 focus:ring-primary/50"
            data-testid="nav-proxy-keys"
          >
            <div class="min-w-0">
              <div
                class="font-medium flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="mynaui:key" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.proxyKeysCardTitle") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">{{ t("settings.proxyKeysCardDesc") }}</p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 shrink-0"
            />
          </div>
          <div class="flex items-center justify-between gap-4 p-4">
            <div class="min-w-0">
              <div class="font-medium flex items-center gap-2">
                <Icon icon="mynaui:chart-bar-one" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.usageAndQuota") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">{{ t("settings.usageAndQuotaDesc") }}</p>
            </div>
            <button class="btn btn-outline btn-sm shrink-0" @click="isQuotaModalOpen = true">
              {{ t("settings.checkQuota") }}
            </button>
          </div>
        </div>
      </section>

      <!-- Group: System -->
      <section class="space-y-2">
        <h2 class="px-1 text-xs font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.systemActions") }}
        </h2>
        <div class="rounded-xl border border-base-300 bg-base-200/50 divide-y divide-base-300">
          <div
            role="button"
            tabindex="0"
            @click="go('/settings/runners')"
            @keydown.enter.space.prevent="go('/settings/runners')"
            class="group flex items-center justify-between gap-4 p-4 cursor-pointer hover:bg-base-200 transition-colors focus:outline-hidden focus:ring-2 focus:ring-primary/50 rounded-t-xl"
            data-testid="nav-runners"
          >
            <div class="min-w-0">
              <div
                class="font-medium flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="material-symbols:directions-run" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.runnersCardTitle") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">{{ t("settings.runnersCardDesc") }}</p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 shrink-0"
            />
          </div>
          <div
            v-if="isAdmin"
            role="button"
            tabindex="0"
            @click="go('/settings/logs')"
            @keydown.enter.space.prevent="go('/settings/logs')"
            class="group flex items-center justify-between gap-4 p-4 cursor-pointer hover:bg-base-200 transition-colors focus:outline-hidden focus:ring-2 focus:ring-primary/50"
          >
            <div class="min-w-0">
              <div
                class="font-medium flex items-center gap-2 group-hover:text-primary transition-colors"
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
              <p class="text-xs text-base-content/70 mt-1">
                {{ t("settings.logsAndDiagnosticsDesc") }}
              </p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 shrink-0"
            />
          </div>
          <div
            role="button"
            tabindex="0"
            @click="go('/settings/compression')"
            @keydown.enter.space.prevent="go('/settings/compression')"
            class="group flex items-center justify-between gap-4 p-4 cursor-pointer hover:bg-base-200 transition-colors focus:outline-hidden focus:ring-2 focus:ring-primary/50 rounded-b-xl"
          >
            <div class="min-w-0">
              <div
                class="font-medium flex items-center gap-2 group-hover:text-primary transition-colors"
              >
                <Icon icon="mynaui:chart-bar-one" class="w-5 h-5 text-primary" />
                <span>{{ t("settings.compressionStats") }}</span>
              </div>
              <p class="text-xs text-base-content/70 mt-1">
                {{ t("settings.compressionStatsDesc") }}
              </p>
            </div>
            <Icon
              icon="material-symbols:chevron-right"
              class="w-6 h-6 text-base-content/40 shrink-0"
            />
          </div>
        </div>
      </section>
    </div>

    <!-- Quota Modal -->
    <QuotaModal v-model="isQuotaModalOpen" />
  </div>
</template>
