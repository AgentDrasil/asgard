<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import {
  getUserProxyKeys,
  saveUserProxyKey,
  deleteUserProxyKey,
  type UserProxyKeyMeta,
} from "../lib/api";
import { useToast } from "../composables/useToast";
import { formatRelativeTime } from "../i18n/timeUtils";

const KEY_NAME_REGEX = /^[A-Za-z_][A-Za-z0-9_]*$/;
const PROTECTED_KEYS = new Set([
  "PATH",
  "LD_PRELOAD",
  "LD_LIBRARY_PATH",
  "IFS",
  "SHELL",
  "HOME",
  "USER",
  "LOGNAME",
  "HTTP_PROXY",
  "HTTPS_PROXY",
  "ALL_PROXY",
  "NO_PROXY",
  "SSL_CERT_FILE",
  "REQUESTS_CA_BUNDLE",
  "NODE_EXTRA_CA_CERTS",
  "CURL_CA_BUNDLE",
]);

const { t } = useI18n();
const toast = useToast();

const keys = ref<UserProxyKeyMeta[]>([]);
const loading = ref(false);
const isModalOpen = ref(false);
const isEditing = ref(false);
const isSubmitting = ref(false);
const formKeyName = ref("");
const formKeyValue = ref("");
const showSecret = ref(false);
const deleteTargetKey = ref<string | null>(null);

const keyNameError = computed(() => {
  const name = formKeyName.value.trim();
  if (!name) return "";
  if (name.length > 128) {
    return t("settings.proxySecretsKeyNameTooLong");
  }
  if (!KEY_NAME_REGEX.test(name)) {
    return t("settings.proxySecretsKeyNameInvalid");
  }
  if (PROTECTED_KEYS.has(name.toUpperCase())) {
    return t("settings.proxySecretsKeyNameReserved");
  }
  return "";
});

const fetchKeys = async () => {
  loading.value = true;
  try {
    keys.value = await getUserProxyKeys();
  } catch (err: any) {
    toast.error(err?.message || "Failed to load proxy keys");
  } finally {
    loading.value = false;
  }
};

onMounted(() => {
  void fetchKeys();
});

const openAddModal = () => {
  isEditing.value = false;
  formKeyName.value = "";
  formKeyValue.value = "";
  showSecret.value = false;
  isModalOpen.value = true;
};

const openEditModal = (keyName: string) => {
  isEditing.value = true;
  formKeyName.value = keyName;
  formKeyValue.value = "";
  showSecret.value = false;
  isModalOpen.value = true;
};

const closeModal = () => {
  isModalOpen.value = false;
  formKeyName.value = "";
  formKeyValue.value = "";
  showSecret.value = false;
};

const handleSave = async () => {
  const keyName = formKeyName.value.trim();
  const val = formKeyValue.value.replace(/[\r\n]+$/, "");
  if (!keyName || !val || keyNameError.value) return;

  isSubmitting.value = true;
  try {
    await saveUserProxyKey(keyName, val);
    toast.success(t("settings.proxySecretsSaveSuccess"));
    closeModal();
    await fetchKeys();
  } catch (err: any) {
    toast.error(err?.message || "Failed to save key");
  } finally {
    isSubmitting.value = false;
  }
};

const confirmDelete = (keyName: string) => {
  deleteTargetKey.value = keyName;
};

const cancelDelete = () => {
  deleteTargetKey.value = null;
};

const handleDelete = async () => {
  const keyName = deleteTargetKey.value;
  if (!keyName) return;

  isSubmitting.value = true;
  try {
    await deleteUserProxyKey(keyName);
    toast.success(t("settings.proxySecretsDeleteSuccess"));
    deleteTargetKey.value = null;
    await fetchKeys();
  } catch (err: any) {
    toast.error(err?.message || "Failed to delete key");
  } finally {
    isSubmitting.value = false;
  }
};
</script>

<template>
  <section class="space-y-3" data-testid="user-proxy-secrets-section">
    <div class="flex items-center justify-between gap-2">
      <div class="space-y-0.5">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          {{ t("settings.proxySecrets") }}
        </h2>
      </div>
      <button
        @click="openAddModal"
        class="btn btn-primary btn-xs gap-1.5"
        data-testid="add-proxy-key-btn"
      >
        <Icon icon="mynaui:plus" class="w-4 h-4" />
        <span>{{ t("settings.proxySecretsAddKey") }}</span>
      </button>
    </div>

    <div class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 space-y-4">
      <p class="text-xs text-base-content/70">
        {{ t("settings.proxySecretsDesc") }}
      </p>

      <!-- Loading skeleton / spinner -->
      <div v-if="loading" class="flex justify-center py-4">
        <span class="loading loading-spinner loading-sm text-primary"></span>
      </div>

      <!-- Empty state -->
      <div
        v-else-if="keys.length === 0"
        class="text-center py-6 text-base-content/60 space-y-1"
        data-testid="proxy-keys-empty"
      >
        <Icon icon="mynaui:key" class="w-8 h-8 mx-auto text-base-content/40" />
        <p class="text-xs">{{ t("settings.proxySecretsEmpty") }}</p>
      </div>

      <!-- Keys list -->
      <ul v-else class="divide-y divide-base-300" data-testid="proxy-keys-list">
        <li
          v-for="k in keys"
          :key="k.key_name"
          class="py-2.5 flex items-center justify-between gap-3"
          data-testid="proxy-key-row"
        >
          <div class="flex items-center gap-2.5 min-w-0">
            <Icon icon="mynaui:key" class="w-4 h-4 text-primary shrink-0" />
            <div class="min-w-0">
              <span class="font-mono text-sm font-medium text-base-content truncate block">
                {{ k.key_name }}
              </span>
              <span class="text-xs text-base-content/60">
                {{ t("settings.proxySecretsLastUpdated") }}:
                {{ formatRelativeTime(k.updated_at, t) }}
              </span>
            </div>
          </div>
          <div class="flex items-center gap-1.5 shrink-0">
            <button
              @click="openEditModal(k.key_name)"
              class="btn btn-ghost btn-xs btn-square"
              :title="t('settings.proxySecretsEdit')"
              :aria-label="t('settings.proxySecretsEdit')"
              data-testid="edit-proxy-key-btn"
            >
              <Icon
                icon="mynaui:edit-one"
                class="w-4 h-4 text-base-content/70 hover:text-base-content"
              />
            </button>
            <button
              @click="confirmDelete(k.key_name)"
              class="btn btn-ghost btn-xs btn-square text-error/80 hover:text-error"
              :title="t('settings.proxySecretsDelete')"
              :aria-label="t('settings.proxySecretsDelete')"
              data-testid="delete-proxy-key-btn"
            >
              <Icon icon="mynaui:trash-one" class="w-4 h-4" />
            </button>
          </div>
        </li>
      </ul>
    </div>

    <!-- Add/Edit Key Modal -->
    <div
      v-if="isModalOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-xs p-4"
      data-testid="proxy-key-modal"
    >
      <div
        class="bg-base-100 rounded-2xl border border-base-300 shadow-xl max-w-md w-full p-6 space-y-4"
      >
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2 font-semibold text-base">
            <Icon icon="mynaui:key" class="w-5 h-5 text-primary" />
            <span>{{
              isEditing ? t("settings.proxySecretsEditKey") : t("settings.proxySecretsAddKey")
            }}</span>
          </div>
          <button @click="closeModal" class="btn btn-ghost btn-xs btn-square">
            <Icon icon="mynaui:x" class="w-4 h-4" />
          </button>
        </div>

        <form @submit.prevent="handleSave" class="space-y-4">
          <div class="space-y-1">
            <label class="text-xs font-medium text-base-content/80">
              {{ t("settings.proxySecretsKeyName") }}
            </label>
            <input
              v-model="formKeyName"
              type="text"
              :readonly="isEditing"
              :placeholder="t('settings.proxySecretsKeyNamePlaceholder')"
              class="input input-sm input-bordered w-full font-mono text-sm"
              :class="{ 'opacity-70 bg-base-200': isEditing, 'input-error': !!keyNameError }"
              required
              data-testid="proxy-key-name-input"
            />
            <p
              v-if="keyNameError"
              class="text-xs text-error mt-1"
              data-testid="proxy-key-name-error"
            >
              {{ keyNameError }}
            </p>
          </div>

          <div class="space-y-1">
            <label class="text-xs font-medium text-base-content/80">
              {{ t("settings.proxySecretsKeyValue") }}
            </label>
            <div class="relative">
              <input
                v-model="formKeyValue"
                :type="showSecret ? 'text' : 'password'"
                :placeholder="t('settings.proxySecretsKeyValuePlaceholder')"
                class="input input-sm input-bordered w-full font-mono text-sm pr-9"
                required
                data-testid="proxy-key-value-input"
              />
              <button
                type="button"
                @click="showSecret = !showSecret"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-base-content/50 hover:text-base-content"
                tabindex="-1"
              >
                <Icon :icon="showSecret ? 'mynaui:eye-slash' : 'mynaui:eye'" class="w-4 h-4" />
              </button>
            </div>
          </div>

          <div class="flex justify-end gap-2 pt-2">
            <button
              type="button"
              @click="closeModal"
              class="btn btn-ghost btn-sm"
              :disabled="isSubmitting"
            >
              {{ t("settings.proxySecretsCancel") }}
            </button>
            <button
              type="submit"
              class="btn btn-primary btn-sm gap-1.5"
              :disabled="
                isSubmitting ||
                !formKeyName.trim() ||
                !formKeyValue.replace(/[\r\n]+$/, '') ||
                !!keyNameError
              "
              data-testid="proxy-key-save-btn"
            >
              <span v-if="isSubmitting" class="loading loading-spinner loading-xs"></span>
              <span>{{ t("settings.proxySecretsSave") }}</span>
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- Delete Confirmation Modal -->
    <div
      v-if="deleteTargetKey"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-xs p-4"
      data-testid="proxy-key-delete-modal"
    >
      <div
        class="bg-base-100 rounded-2xl border border-base-300 shadow-xl max-w-sm w-full p-5 space-y-4"
      >
        <div class="flex items-center gap-2 text-error font-semibold text-base">
          <Icon icon="mynaui:danger" class="w-5 h-5" />
          <span>{{ t("settings.proxySecretsDelete") }}</span>
        </div>
        <p class="text-sm text-base-content/80">
          {{ t("settings.proxySecretsDeleteConfirm", { key: deleteTargetKey }) }}
        </p>
        <div class="flex justify-end gap-2 pt-2">
          <button
            type="button"
            @click="cancelDelete"
            class="btn btn-ghost btn-sm"
            :disabled="isSubmitting"
          >
            {{ t("settings.proxySecretsCancel") }}
          </button>
          <button
            type="button"
            @click="handleDelete"
            class="btn btn-error btn-sm gap-1.5"
            :disabled="isSubmitting"
            data-testid="proxy-key-confirm-delete-btn"
          >
            <span v-if="isSubmitting" class="loading loading-spinner loading-xs"></span>
            <span>{{ t("settings.proxySecretsDelete") }}</span>
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
