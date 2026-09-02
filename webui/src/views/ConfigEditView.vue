<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from "vue";
import { useRouter, onBeforeRouteLeave } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { getConfigFile, saveConfigFile } from "../lib/api";
import { useToast } from "../composables/useToast";
import { useRestartFlow } from "../composables/useRestartFlow";

const router = useRouter();
const { t } = useI18n();
const toast = useToast();

const {
  isRestarting,
  isRestartConfirmOpen,
  openRestartConfirm,
  closeRestartConfirm,
  triggerRestartWorkflow,
} = useRestartFlow();

const loading = ref(false);
const saving = ref(false);
const filePath = ref("");
const configContent = ref("");
const originalContent = ref("");
const errorMessage = ref("");
const saveSuccess = ref(false);

watch(configContent, (newVal) => {
  if (saveSuccess.value && newVal !== originalContent.value) {
    saveSuccess.value = false;
  }
});

const loadConfig = async () => {
  loading.value = true;
  errorMessage.value = "";
  saveSuccess.value = false;
  try {
    const res = await getConfigFile();
    if (res) {
      filePath.value = res.path;
      configContent.value = res.content;
      originalContent.value = res.content;
    } else {
      errorMessage.value = t("config.loadFailed");
    }
  } catch (err: any) {
    errorMessage.value = err?.message || t("config.loadError");
  } finally {
    loading.value = false;
  }
};

const handleReloadFromDisk = () => {
  if (isDirty() && !window.confirm(t("config.reloadConfirm"))) {
    return;
  }
  void loadConfig();
};

const handleSave = async () => {
  saving.value = true;
  errorMessage.value = "";
  saveSuccess.value = false;
  try {
    const res = await saveConfigFile(configContent.value);
    if (res.error) {
      errorMessage.value = res.error;
      toast.error(res.error, { title: t("config.saveFailed") });
    } else {
      saveSuccess.value = true;
      originalContent.value = configContent.value;
      toast.success(t("config.saveSuccess"), {
        title: t("config.saveSuccessTitle"),
      });
    }
  } catch (err: any) {
    errorMessage.value = err?.message || t("config.saveGenericError");
    toast.error(errorMessage.value, { title: t("config.saveFailed") });
  } finally {
    saving.value = false;
  }
};

const isDirty = () => configContent.value !== originalContent.value;

onBeforeRouteLeave(() => {
  if (!isDirty()) return true;
  return window.confirm(t("config.unsavedChangesConfirm"));
});

const handleBack = () => {
  if (window.history.state?.back) {
    router.back();
  } else {
    router.push("/settings");
  }
};

const handleBeforeUnload = (e: BeforeUnloadEvent) => {
  if (isDirty()) {
    e.preventDefault();
    e.returnValue = "";
  }
};

onMounted(() => {
  void loadConfig();
  window.addEventListener("beforeunload", handleBeforeUnload);
});

onUnmounted(() => {
  window.removeEventListener("beforeunload", handleBeforeUnload);
});
</script>

<template>
  <div class="flex flex-col h-full w-full bg-base-100 overflow-hidden">
    <!-- Header Toolbar -->
    <header
      class="sticky top-0 z-20 flex items-center justify-between border-b border-base-300 bg-base-100/90 px-4 py-3 backdrop-blur md:px-6"
    >
      <div class="flex items-center gap-3 min-w-0">
        <button
          @click="handleBack"
          class="btn btn-ghost btn-sm btn-square"
          :title="t('config.backToSettings')"
          :aria-label="t('config.backToSettings')"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2 min-w-0">
          <Icon icon="mynaui:cog" class="w-5 h-5 text-primary shrink-0" />
          <div class="min-w-0">
            <h1 class="text-base font-semibold md:text-lg leading-tight truncate">
              {{ t("config.title") }}
            </h1>
            <span v-if="filePath" class="text-xs text-base-content/60 font-mono block truncate">{{
              filePath
            }}</span>
          </div>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <button
          @click="handleReloadFromDisk"
          class="btn btn-ghost btn-sm gap-1.5"
          :disabled="loading || saving"
          :title="t('config.reloadTitle')"
        >
          <Icon icon="mynaui:refresh" :class="['w-4 h-4', { 'animate-spin': loading }]" />
          <span class="hidden sm:inline">{{ t("config.reload") }}</span>
        </button>

        <button
          @click="handleSave"
          class="btn btn-primary btn-sm gap-1.5"
          :disabled="loading || saving"
        >
          <span v-if="saving" class="loading loading-spinner loading-xs"></span>
          <Icon v-else icon="mynaui:save" class="w-4 h-4" />
          <span>{{ t("config.save") }}</span>
        </button>
      </div>
    </header>

    <!-- Body Area -->
    <div class="flex-1 flex flex-col p-4 md:p-6 overflow-hidden gap-4 max-w-6xl w-full mx-auto">
      <!-- Loading State -->
      <div v-if="loading" class="flex flex-col items-center justify-center py-20 space-y-3 flex-1">
        <span class="loading loading-spinner loading-lg text-primary"></span>
        <span class="text-sm text-base-content/70">{{ t("config.loading") }}</span>
      </div>

      <template v-else>
        <!-- Validation Error Banner -->
        <div v-if="errorMessage" class="alert alert-error flex items-start gap-3 text-sm shrink-0">
          <Icon icon="mynaui:danger" class="h-5 w-5 shrink-0 mt-0.5" />
          <div class="flex-1 min-w-0">
            <h4 class="font-bold">{{ t("config.validationError") }}</h4>
            <pre class="text-xs whitespace-pre-wrap font-mono mt-1 overflow-x-auto">{{
              errorMessage
            }}</pre>
          </div>
        </div>

        <!-- Success Banner with Restart Server Action -->
        <div
          v-if="saveSuccess"
          class="alert alert-success flex items-center justify-between gap-3 text-sm shrink-0"
        >
          <div class="flex items-center gap-2">
            <Icon icon="mynaui:check-circle" class="h-5 w-5 shrink-0" />
            <span>{{ t("config.saveSuccessBanner") }}</span>
          </div>
          <button
            @click="openRestartConfirm"
            class="btn btn-sm btn-outline border-success-content/30 text-success-content hover:bg-success-content/20 gap-1.5"
            :disabled="isRestarting"
          >
            <Icon icon="mynaui:power" :class="['h-4 w-4', { 'animate-spin': isRestarting }]" />
            <span>{{ t("config.restartServer") }}</span>
          </button>
        </div>

        <!-- Code Editor Area -->
        <div
          class="flex-1 flex flex-col min-h-0 rounded-xl border border-base-300 overflow-hidden bg-base-200/50"
        >
          <textarea
            v-model="configContent"
            class="textarea w-full h-full resize-none border-0 rounded-none font-mono text-xs md:text-sm p-4 leading-relaxed bg-transparent focus:outline-hidden text-base-content"
            :placeholder="t('config.placeholder')"
            spellcheck="false"
          ></textarea>
        </div>
      </template>
    </div>

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
