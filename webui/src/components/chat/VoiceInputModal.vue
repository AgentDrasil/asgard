<script setup lang="ts">
import { computed } from "vue";
import { Icon } from "@iconify/vue";
import { t } from "../../i18n";

const props = defineProps<{
  isOpen: boolean;
  isRecording: boolean;
  isConnecting: boolean;
  isStopping: boolean;
  livePreviewText: string;
  interimText: string;
  shortcutDisplay?: string;
}>();

const emit = defineEmits<{
  (e: "stop"): void;
  (e: "cancel"): void;
}>();

const displayText = computed(() => {
  return props.livePreviewText || props.interimText || "";
});

const isBusy = computed(() => props.isConnecting || props.isStopping);

const handleStop = () => {
  if (!props.isStopping) {
    emit("stop");
  }
};

const handleCancel = () => {
  emit("cancel");
};
</script>

<template>
  <dialog class="modal modal-bottom sm:modal-middle" :class="{ 'modal-open': isOpen }">
    <div
      class="modal-box w-full max-w-xl p-5 sm:p-6 bg-base-100 text-base-content border border-base-300 shadow-2xl rounded-t-3xl sm:rounded-2xl flex flex-col gap-4 max-h-[85vh] sm:max-h-[75vh]"
      data-testid="voice-input-modal"
    >
      <!-- Header -->
      <div class="flex items-center justify-between pb-3 border-b border-base-200">
        <div class="flex items-center gap-2.5">
          <div
            class="relative flex items-center justify-center w-8 h-8 rounded-full bg-error/15 text-error"
          >
            <span
              v-if="isRecording"
              class="absolute inline-flex h-full w-full rounded-full bg-error opacity-40 animate-ping"
            ></span>
            <Icon icon="material-symbols:mic" class="w-5 h-5 relative z-10" />
          </div>
          <div>
            <h3 class="font-bold text-base sm:text-lg flex items-center gap-2">
              {{ t("chat.voiceModalTitle") }}
              <span
                v-if="shortcutDisplay"
                class="kbd kbd-sm text-2xs font-mono font-normal opacity-70 hidden sm:inline-flex"
                :title="shortcutDisplay"
              >
                {{ shortcutDisplay }}
              </span>
            </h3>
            <p class="text-xs text-base-content/60">{{ t("chat.voiceModalDesc") }}</p>
          </div>
        </div>

        <button
          type="button"
          @click="handleCancel"
          class="btn btn-sm btn-circle btn-ghost"
          :title="t('chat.voiceCancel')"
          data-testid="voice-modal-close-btn"
        >
          <Icon icon="material-symbols:close" class="h-5 w-5" />
        </button>
      </div>

      <!-- Live Wave / Status Indicator -->
      <div
        class="flex items-center justify-between px-3 py-2 rounded-xl bg-base-200/70 border border-base-300/50 text-xs text-base-content/80"
      >
        <div class="flex items-center gap-2 min-w-0">
          <span v-if="isConnecting" class="loading loading-spinner loading-xs text-warning"></span>
          <span v-else-if="isStopping" class="loading loading-spinner loading-xs text-info"></span>
          <span
            v-else-if="isRecording"
            class="w-2.5 h-2.5 rounded-full bg-error animate-pulse shrink-0"
          ></span>
          <span class="font-medium truncate">
            {{
              isConnecting
                ? t("chat.voiceConnecting")
                : isStopping
                  ? t("chat.voiceStopping")
                  : t("chat.recording")
            }}
          </span>
        </div>
        <span class="text-2xs opacity-60 shrink-0 hidden sm:inline">
          {{ t("chat.voiceActiveTip") }}
        </span>
      </div>

      <!-- Large Pending Text Transcription Area -->
      <div
        class="flex-1 min-h-[140px] max-h-[300px] sm:min-h-[180px] p-4 rounded-xl bg-base-200/50 border border-base-300 flex flex-col justify-start overflow-y-auto"
        data-testid="voice-modal-text-container"
      >
        <p
          v-if="displayText"
          class="text-base sm:text-xl font-medium leading-relaxed tracking-wide text-base-content whitespace-pre-wrap break-words select-text"
          data-testid="voice-modal-pending-text"
        >
          {{ displayText }}
        </p>
        <div
          v-else
          class="flex flex-col items-center justify-center h-full text-base-content/40 py-6"
        >
          <div class="flex items-center gap-1.5 mb-2">
            <span
              class="w-1.5 h-3 bg-primary/40 rounded-full animate-bounce [animation-delay:-0.3s]"
            ></span>
            <span
              class="w-1.5 h-5 bg-primary/60 rounded-full animate-bounce [animation-delay:-0.15s]"
            ></span>
            <span class="w-1.5 h-4 bg-primary/40 rounded-full animate-bounce"></span>
          </div>
          <p class="text-sm italic">{{ t("chat.voiceInterimPlaceholder") }}</p>
        </div>
      </div>

      <!-- Actions Footer -->
      <div class="flex items-center justify-between gap-3 pt-2 border-t border-base-200">
        <button
          type="button"
          @click="handleCancel"
          class="btn btn-ghost btn-sm sm:btn-md gap-1.5 text-base-content/70 hover:text-base-content"
          data-testid="voice-modal-cancel-btn"
        >
          <Icon icon="material-symbols:cancel-outline" class="w-4 h-4 sm:w-5 sm:h-5" />
          <span>{{ t("chat.voiceCancel") }}</span>
        </button>

        <button
          type="button"
          @click="handleStop"
          :disabled="isStopping"
          class="btn btn-error btn-sm sm:btn-md px-5 sm:px-6 shadow-md gap-2 font-medium"
          data-testid="voice-modal-stop-btn"
        >
          <span v-if="isBusy" class="loading loading-spinner loading-xs"></span>
          <Icon v-else icon="akar-icons:square-fill" class="w-4 h-4 fill-current" />
          <span>{{ t("chat.stopRecording") }}</span>
        </button>
      </div>
    </div>
    <form method="dialog" class="modal-backdrop">
      <button type="button" @click="handleCancel">close</button>
    </form>
  </dialog>
</template>
