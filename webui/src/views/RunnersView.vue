<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import RunnersPanel from "../components/RunnersPanel.vue";
import RunnerAdminPanel from "../components/RunnerAdminPanel.vue";
import { useUserProfile } from "../composables/useUserProfile";
import { isAuthEnabled } from "../lib/api";

const router = useRouter();
const { t } = useI18n();

const authEnabled = isAuthEnabled();
const { profile, load: loadProfile } = useUserProfile();
const isAdmin = computed(() => !authEnabled || Boolean(profile.value?.isAdmin));

onMounted(() => {
  void loadProfile();
});

const navigateBack = () => {
  if (window.history.state?.back) {
    router.back();
  } else {
    router.push("/settings");
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
          :title="t('settings.backToSettings')"
          :aria-label="t('settings.backToSettings')"
          data-testid="runners-back-btn"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2">
          <Icon icon="mynaui:server" class="w-5 h-5 text-primary" />
          <h1 class="text-base font-semibold md:text-lg">{{ t("runners.title") }}</h1>
        </div>
      </div>
    </header>

    <!-- Main Content -->
    <div class="p-4 md:p-6 max-w-4xl w-full mx-auto space-y-6 flex-1">
      <!-- Accessible Runners Panel (For all users: shows status, online/offline, last seen, etc.) -->
      <RunnersPanel />

      <!-- Admin User Assignment Panel (For admins: searchable user assignment with name, email, avatar) -->
      <RunnerAdminPanel v-if="isAdmin" />
    </div>
  </div>
</template>
