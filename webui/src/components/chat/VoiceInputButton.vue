<script setup lang="ts">
import { computed } from "vue";
import { Icon } from "@iconify/vue";
import { t } from "../../i18n";

const props = defineProps<{
  isRecording: boolean;
  isConnecting: boolean;
  isStopping: boolean;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  (e: "toggle"): void;
}>();

const isActionDisabled = computed(() => {
  return Boolean(props.disabled || props.isStopping);
});

const buttonTitle = computed(() => {
  if (props.isStopping) {
    return t("chat.voiceStopping");
  }
  if (props.isRecording) {
    return t("chat.stopRecording");
  }
  if (props.isConnecting) {
    return t("chat.voiceConnecting");
  }
  return t("chat.startRecording");
});

const handleClick = () => {
  if (!isActionDisabled.value) {
    emit("toggle");
  }
};
</script>

<template>
  <button
    type="button"
    :disabled="isActionDisabled"
    @click="handleClick"
    class="btn btn-circle btn-sm transition-all shadow-sm"
    :class="[
      isRecording
        ? 'btn-error animate-pulse text-white'
        : 'btn-ghost hover:bg-base-300 text-base-content/70 hover:text-base-content hover:scale-105 active:scale-95',
    ]"
    :title="buttonTitle"
    :aria-label="t('chat.voiceInput')"
  >
    <span v-if="isConnecting || isStopping" class="loading loading-spinner loading-xs"></span>
    <Icon v-else-if="isRecording" icon="material-symbols:graphic-eq" class="h-4 w-4 fill-current" />
    <Icon v-else icon="material-symbols:mic" class="h-4 w-4 fill-current" />
  </button>
</template>
