<script setup lang="ts">
import { ref, watch, computed, onBeforeUnmount } from "vue";
import { Icon } from "@iconify/vue";
import { useShortcuts } from "../composables/useShortcuts";
import { useToast } from "../composables/useToast";
import { useVoiceInput } from "../composables/useVoiceInput";
import type { Attachment, VoiceErrorCode } from "../types";
import { uploadAttachment } from "../lib/api";
import AttachmentChips from "./chat/AttachmentChips.vue";
import VoiceInputButton from "./chat/VoiceInputButton.vue";
import { t } from "../i18n";

const { modKey, sendShortcut, matchShortcut } = useShortcuts();
const toast = useToast();

const props = withDefaults(
  defineProps<{
    loading: boolean;
    modelValue?: string;
    sessionId?: string | null;
    queuedCount?: number;
    isRunning?: boolean;
    activeAgentType?: string;
  }>(),
  {
    queuedCount: 0,
    isRunning: false,
    activeAgentType: "agent",
  },
);

const emit = defineEmits<{
  (e: "send", text: string, attachments?: Attachment[]): void;
  (e: "update:modelValue", value: string): void;
}>();

const text = ref(props.modelValue ?? "");
const isModalOpen = ref(false);
const attachments = ref<Attachment[]>([]);
const isUploading = ref(false);
const isDragging = ref(false);
let dragEnterCounter = 0;
const fileInputRef = ref<HTMLInputElement | null>(null);

const isQueueMode = computed(() => {
  return (props.isRunning || (props.queuedCount || 0) > 0) && props.activeAgentType !== "workflow";
});

const isQueueFull = computed(() => {
  return isQueueMode.value && (props.queuedCount || 0) >= 3;
});

const isInputDisabled = computed(() => {
  return isQueueMode.value ? isQueueFull.value : props.loading;
});

const placeholderText = computed(() => {
  if (isQueueFull.value) {
    return t("chat.queueLimitPlaceholder");
  }
  if (isQueueMode.value) {
    return t("chat.enqueueTip");
  }
  return t("chat.inputPlaceholder", { shortcut: sendShortcut.value });
});

const isMultiline = computed(() => {
  return text.value.includes("\n");
});

const lineCount = computed(() => {
  if (!text.value) return 0;
  return text.value.split("\n").length;
});

const canSend = computed(() => {
  if (isInputDisabled.value || isUploading.value) return false;
  return text.value.trim().length > 0;
});

// Sync when parent pushes a new value (e.g. appending diff comments)
watch(
  () => props.modelValue,
  (v) => {
    if (v !== undefined && v !== text.value) {
      text.value = v;
    }
  },
);

// Emit upward whenever text changes so parent stays in sync
watch(text, (v) => {
  emit("update:modelValue", v);
});

const handleKeyDown = (e: KeyboardEvent) => {
  if (matchShortcut(e, "send_message")) {
    e.preventDefault();
    handleSend();
  }
};

const handleSend = () => {
  if (!canSend.value) return;
  const currentAttachments = attachments.value.length > 0 ? [...attachments.value] : undefined;
  emit("send", text.value.trim(), currentAttachments);
  text.value = "";
  attachments.value = [];
  isModalOpen.value = false;
};

const VOICE_ERROR_I18N_MAP: Record<VoiceErrorCode, string> = {
  micDenied: "chat.micPermissionDenied",
  voiceUnavailable: "chat.voiceUnavailable",
  sessionTimeout: "chat.voiceSessionTimeout",
  network: "chat.voiceUnavailable",
};

const handleVoiceFinalText = (finalText: string) => {
  const trimmed = finalText.trim();
  if (!trimmed) return; // no-op if empty
  if (!text.value) {
    text.value = trimmed;
  } else if (/\s$/.test(text.value)) {
    text.value += trimmed;
  } else {
    text.value += " " + trimmed;
  }
};

const {
  isRecording,
  isConnecting,
  isStopping,
  interimText,
  livePreviewText,
  error: voiceError,
  startRecording,
  stopRecording,
  cancelRecording,
} = useVoiceInput({
  onFinalText: handleVoiceFinalText,
});

watch(voiceError, (code) => {
  if (code) {
    const key = VOICE_ERROR_I18N_MAP[code] || "chat.voiceUnavailable";
    toast.error(t(key));
  }
});

const handleVoiceToggle = () => {
  if (isRecording.value || isConnecting.value) {
    void stopRecording();
  } else {
    void startRecording();
  }
};

onBeforeUnmount(() => {
  cancelRecording();
});

const openModal = () => {
  isModalOpen.value = true;
};

const closeModal = () => {
  if (isRecording.value) {
    cancelRecording();
  }
  isModalOpen.value = false;
};

const triggerFileInput = () => {
  if (!props.sessionId || isUploading.value || props.loading || isQueueMode.value) return;
  fileInputRef.value?.click();
};

const MAX_ATTACHMENTS = 20;
const MAX_SINGLE_FILE_SIZE = 20 * 1024 * 1024; // 20MB
const MAX_TOTAL_FILES_SIZE = 50 * 1024 * 1024; // 50MB

const processFiles = async (files: FileList | File[]) => {
  if (!props.sessionId || !files || files.length === 0 || isQueueMode.value) return;

  const fileArray = Array.from(files);
  if (attachments.value.length + fileArray.length > MAX_ATTACHMENTS) {
    toast.error(t("chat.maxAttachmentsExceeded", { max: MAX_ATTACHMENTS }));
    return;
  }

  let currentTotalSize = attachments.value.reduce((sum, a) => sum + a.size, 0);

  isUploading.value = true;
  try {
    for (let i = 0; i < fileArray.length; i++) {
      const file = fileArray[i];
      if (!file) continue;

      if (file.size > MAX_SINGLE_FILE_SIZE) {
        toast.error(t("chat.fileSizeExceeded", { name: file.name }));
        continue;
      }

      if (currentTotalSize + file.size > MAX_TOTAL_FILES_SIZE) {
        toast.error(t("chat.totalSizeExceeded"));
        continue;
      }

      // Avoid duplicate file (same name & size)
      const isDuplicate = attachments.value.some(
        (existing) => existing.name === file.name && existing.size === file.size,
      );
      if (isDuplicate) {
        continue;
      }

      try {
        const att = await uploadAttachment(props.sessionId, file);
        if (att) {
          // Avoid duplicate attachment name in list if response differs
          if (!attachments.value.some((a) => a.name === att.name && a.size === att.size)) {
            attachments.value.push(att);
            currentTotalSize += file.size;
          }
        }
      } catch (err: any) {
        console.error("Failed to upload attachment:", err);
        toast.error(
          t("chat.failedToUpload", {
            name: file.name,
            error: err?.message || t("chat.uploadFailed"),
          }),
        );
      }
    }
  } finally {
    isUploading.value = false;
    if (fileInputRef.value) {
      fileInputRef.value.value = "";
    }
  }
};

const handleFileChange = (e: Event) => {
  const target = e.target as HTMLInputElement;
  if (target.files && target.files.length > 0) {
    void processFiles(target.files);
  }
};

const removeAttachment = (index: number) => {
  attachments.value.splice(index, 1);
};

const handlePaste = (e: ClipboardEvent) => {
  if (!props.sessionId || !e.clipboardData || !e.clipboardData.files || isQueueMode.value) return;
  const files = e.clipboardData.files;
  if (files.length > 0) {
    e.preventDefault();
    void processFiles(files);
  }
};

const handleDragEnter = (e: DragEvent) => {
  if (!props.sessionId || props.loading || isQueueMode.value) return;
  if (e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files")) {
    dragEnterCounter++;
    isDragging.value = true;
  }
};

const handleDragOver = (e: DragEvent) => {
  if (!props.sessionId || props.loading || isQueueMode.value) return;
  if (e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files")) {
    e.preventDefault();
  }
};

const handleDragLeave = (_e: DragEvent) => {
  if (!props.sessionId || props.loading || isQueueMode.value) return;
  dragEnterCounter--;
  if (dragEnterCounter <= 0) {
    dragEnterCounter = 0;
    isDragging.value = false;
  }
};

const handleDrop = (e: DragEvent) => {
  dragEnterCounter = 0;
  isDragging.value = false;
  if (!props.sessionId || props.loading || isQueueMode.value) return;
  e.preventDefault();
  if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
    void processFiles(e.dataTransfer.files);
  }
};
</script>

<template>
  <div
    class="p-2.5 sm:p-4 bg-base-100 border-t border-base-200/50 md:border-t-0 shrink-0 min-w-0 w-full"
    @dragenter="handleDragEnter"
    @dragover="handleDragOver"
    @dragleave="handleDragLeave"
    @drop="handleDrop"
    :class="{ 'ring-2 ring-primary ring-inset rounded-lg': isDragging }"
  >
    <!-- Hidden file input -->
    <input ref="fileInputRef" type="file" multiple class="hidden" @change="handleFileChange" />

    <div class="flex flex-col max-w-4xl w-full mx-auto min-w-0 gap-2">
      <!-- Attachment Previews List (Chips) -->
      <AttachmentChips
        :attachments="attachments"
        :is-uploading="isUploading"
        @remove="removeAttachment"
      />

      <!-- Voice Recording Interim Preview Banner -->
      <div
        v-if="isRecording && (livePreviewText || interimText)"
        class="flex items-center gap-2 px-3 py-2 bg-base-200/90 border border-primary/30 rounded-xl text-xs text-base-content/90 animate-fade-in shadow-xs"
        data-testid="voice-preview-banner"
      >
        <span class="loading loading-ring loading-xs text-primary shrink-0"></span>
        <span class="font-medium text-primary shrink-0">{{ $t("chat.recording") }}</span>
        <span class="truncate italic opacity-85">
          {{ livePreviewText || interimText || $t("chat.voiceInterimPlaceholder") }}
        </span>
      </div>

      <!-- Queue Limit Alert Banner -->
      <div
        v-if="isQueueFull"
        class="alert alert-warning py-2 px-3 text-xs rounded-xl shadow-xs flex items-center justify-between"
        data-testid="queue-limit-alert"
      >
        <div class="flex items-center gap-2">
          <Icon icon="material-symbols:warning-rounded" class="w-4 h-4 shrink-0" />
          <span>{{ $t("chat.queueLimitAlert") }}</span>
        </div>
      </div>

      <div class="relative flex items-center w-full min-w-0">
        <!-- Left Buttons Join/Group (Expand, Attach & Voice) -->
        <div class="absolute left-2.5 sm:left-3 z-10 flex items-center gap-1">
          <!-- Expand Button -->
          <button
            @click="openModal"
            type="button"
            class="btn btn-circle btn-sm hover:scale-105 active:scale-95 transition-all shadow-sm"
            :class="
              isMultiline
                ? 'btn-success'
                : 'btn-neutral bg-base-300 text-base-content hover:bg-base-300/80 border-none'
            "
            :title="$t('chat.expandInputEditor')"
          >
            <Icon icon="iconoir:expand" class="h-4 w-4" />
          </button>

          <!-- Attach File Button (Paperclip) -->
          <button
            v-if="sessionId"
            @click="triggerFileInput"
            type="button"
            :disabled="loading || isUploading || isQueueMode"
            class="btn btn-circle btn-sm btn-ghost hover:bg-base-300 text-base-content/70 hover:text-base-content hover:scale-105 active:scale-95 transition-all"
            :title="isQueueMode ? $t('chat.queueTextOnly') : $t('chat.attachFile')"
            data-testid="attach-file-button"
          >
            <Icon icon="material-symbols:attach-file" class="h-4 w-4" />
          </button>

          <!-- Voice Input Button (Standard) -->
          <VoiceInputButton
            :is-recording="isRecording"
            :is-connecting="isConnecting"
            :is-stopping="isStopping"
            :disabled="loading || isUploading"
            @toggle="handleVoiceToggle"
          />
        </div>

        <textarea
          v-model="text"
          @keydown="handleKeyDown"
          @paste="handlePaste"
          :placeholder="placeholderText"
          rows="1"
          :disabled="isInputDisabled"
          class="textarea textarea-bordered bg-base-200 text-base-content w-full rounded-2xl resize-none min-h-[48px] max-h-48 leading-relaxed focus:outline-none focus:border-primary text-base sm:text-sm font-sans placeholder:text-base-content/60"
          :class="sessionId ? 'pl-28 sm:pl-30 pr-11 sm:pr-12' : 'pl-20 sm:pl-22 pr-11 sm:pr-12'"
        ></textarea>

        <!-- Send Button (Right) -->
        <button
          @click="handleSend"
          :disabled="!canSend"
          class="btn btn-circle btn-primary btn-sm absolute right-2.5 sm:right-3 hover:scale-105 active:scale-95 transition-transform"
          :title="
            isQueueMode
              ? $t('chat.enqueueTip')
              : $t('chat.sendMessageShortcut', { shortcut: sendShortcut })
          "
          data-testid="send-message-button"
        >
          <span v-if="loading || isUploading" class="loading loading-spinner loading-xs"></span>
          <Icon v-else icon="material-symbols:send" class="h-4 w-4 fill-current" />
        </button>
      </div>
    </div>

    <!-- Expanded Popup Modal -->
    <dialog class="modal" :class="{ 'modal-open': isModalOpen }">
      <div class="modal-box max-w-3xl w-11/12 flex flex-col max-h-[85vh] p-4 sm:p-6">
        <div class="flex items-center justify-between border-b border-base-300 pb-3 mb-4">
          <div class="flex items-center gap-2">
            <Icon icon="material-symbols:edit-note-rounded" class="h-5 w-5 text-primary" />
            <h3 class="font-bold text-lg">{{ $t("chat.editChatInput") }}</h3>
            <span v-if="lineCount > 1" class="badge badge-sm badge-neutral">
              {{ $t("chat.linesCount", { count: lineCount }) }}
            </span>
          </div>
          <button
            @click="closeModal"
            class="btn btn-sm btn-circle btn-ghost"
            :title="$t('chat.closeModalEsc')"
          >
            <Icon icon="material-symbols:close" class="h-5 w-5" />
          </button>
        </div>

        <!-- Attachments Preview inside Modal -->
        <div class="mb-3">
          <AttachmentChips
            :attachments="attachments"
            :is-uploading="isUploading"
            @remove="removeAttachment"
          />
        </div>

        <!-- Voice Recording Interim Preview Banner inside Modal -->
        <div
          v-if="isRecording && (livePreviewText || interimText)"
          class="flex items-center gap-2 px-3 py-2 mb-3 bg-base-200/90 border border-primary/30 rounded-xl text-xs text-base-content/90 animate-fade-in shadow-xs"
          data-testid="modal-voice-preview-banner"
        >
          <span class="loading loading-ring loading-xs text-primary shrink-0"></span>
          <span class="font-medium text-primary shrink-0">{{ $t("chat.recording") }}</span>
          <span class="truncate italic opacity-85">
            {{ livePreviewText || interimText || $t("chat.voiceInterimPlaceholder") }}
          </span>
        </div>

        <div class="flex-1 min-h-[250px] flex flex-col mb-4">
          <textarea
            v-model="text"
            @keydown="handleKeyDown"
            @paste="handlePaste"
            :placeholder="
              isQueueFull
                ? $t('chat.queueLimitPlaceholder')
                : isQueueMode
                  ? $t('chat.enqueueTip')
                  : $t('chat.promptMultilinePlaceholder', { shortcut: sendShortcut })
            "
            :disabled="isInputDisabled"
            class="textarea textarea-bordered bg-base-200 text-base-content w-full flex-1 p-4 rounded-xl leading-relaxed focus:outline-none focus:border-primary text-sm font-mono resize-none"
          ></textarea>
        </div>

        <div class="flex items-center justify-between gap-2 pt-2 border-t border-base-300">
          <div class="flex items-center gap-2">
            <button
              v-if="sessionId"
              @click="triggerFileInput"
              type="button"
              :disabled="loading || isUploading || isQueueMode"
              class="btn btn-sm btn-ghost gap-1.5 text-base-content/70 hover:text-base-content"
              :title="isQueueMode ? $t('chat.queueTextOnly') : $t('chat.attachFile')"
            >
              <Icon icon="material-symbols:attach-file" class="h-4 w-4" />
              <span class="hidden sm:inline">{{ $t("chat.attach") }}</span>
            </button>

            <!-- Voice Input Button (Modal) -->
            <VoiceInputButton
              :is-recording="isRecording"
              :is-connecting="isConnecting"
              :is-stopping="isStopping"
              :disabled="loading || isUploading"
              @toggle="handleVoiceToggle"
            />

            <span class="text-xs text-base-content/60 hidden sm:inline">
              {{ $t("chat.pressToSend", { mod: modKey }) }}
            </span>
          </div>
          <div class="flex items-center gap-2 ml-auto">
            <button @click="closeModal" class="btn btn-sm btn-ghost">{{ $t("chat.done") }}</button>
            <button
              @click="handleSend"
              :disabled="!canSend"
              class="btn btn-sm btn-primary gap-2"
              :title="
                isQueueMode
                  ? $t('chat.enqueueTip')
                  : $t('chat.sendMessageShortcut', { shortcut: sendShortcut })
              "
            >
              <span v-if="loading || isUploading" class="loading loading-spinner loading-xs"></span>
              <Icon v-else icon="material-symbols:send" class="h-4 w-4" />
              {{ isQueueMode ? $t("chat.enqueue") : $t("chat.send") }}
            </button>
          </div>
        </div>
      </div>
      <form method="dialog" class="modal-backdrop">
        <button @click="closeModal">close</button>
      </form>
    </dialog>
  </div>
</template>
