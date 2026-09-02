<script setup lang="ts">
import { ref } from "vue";
import { Icon } from "@iconify/vue";
import { useToast } from "../../composables/useToast";
import type { ToastItem } from "../../types";

const { toasts, removeToast } = useToast();
const copiedId = ref<string | null>(null);

const copyMessage = async (toast: ToastItem) => {
  try {
    await navigator.clipboard.writeText(toast.message);
    copiedId.value = toast.id;
    setTimeout(() => {
      if (copiedId.value === toast.id) {
        copiedId.value = null;
      }
    }, 1500);
  } catch {
    // clipboard unavailable (e.g. insecure context); silently ignore
  }
};

const alertClass = (type: ToastItem["type"]) => {
  switch (type) {
    case "success":
      return "alert-success";
    case "warning":
      return "alert-warning";
    case "error":
      return "alert-error";
    case "info":
    default:
      return "alert-info";
  }
};

const alertIcon = (type: ToastItem["type"]) => {
  switch (type) {
    case "success":
      return "solar:check-circle-bold";
    case "warning":
      return "solar:danger-triangle-bold";
    case "error":
      return "solar:close-circle-bold";
    case "info":
    default:
      return "solar:info-circle-bold";
  }
};
</script>

<template>
  <div class="toast toast-top toast-end z-50 p-4 gap-2 max-w-md w-full pointer-events-none">
    <div
      v-for="toast in toasts"
      :key="toast.id"
      class="alert shadow-lg pointer-events-auto flex items-start gap-3 py-3 px-4 transition-all duration-200"
      :class="alertClass(toast.type)"
    >
      <Icon :icon="alertIcon(toast.type)" class="w-5 h-5 mt-0.5 shrink-0" />
      <div class="flex-1 min-w-0 text-sm">
        <h4 v-if="toast.title" class="font-bold text-sm leading-tight mb-1">
          {{ toast.title }}
        </h4>
        <div class="whitespace-pre-wrap break-words leading-relaxed text-xs opacity-90">
          {{ toast.message }}
        </div>
      </div>
      <div class="flex items-center gap-1 shrink-0">
        <button
          @click="copyMessage(toast)"
          class="btn btn-ghost btn-xs btn-circle text-current hover:bg-current/15"
          title="Copy notification"
          aria-label="Copy notification"
        >
          <Icon
            :icon="copiedId === toast.id ? 'solar:check-circle-linear' : 'solar:copy-linear'"
            class="w-4 h-4"
          />
        </button>
        <button
          @click="removeToast(toast.id)"
          class="btn btn-ghost btn-xs btn-circle text-current hover:bg-current/15"
          title="Close notification"
          aria-label="Close notification"
        >
          <Icon icon="solar:close-circle-linear" class="w-4 h-4" />
        </button>
      </div>
    </div>
  </div>
</template>
