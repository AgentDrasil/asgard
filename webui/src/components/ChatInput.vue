<script setup lang="ts">
import { ref, watch, computed } from "vue";
import { Icon } from "@iconify/vue";
import { useShortcuts } from "../composables/useShortcuts";
import type { Attachment } from "../types";
import { uploadAttachment } from "../lib/api";
import { formatFileSize } from "../lib/format";
import { getFileIcon } from "../utils/fileUtils";

const { modKey, sendShortcut } = useShortcuts();

const props = defineProps<{
  loading: boolean;
  modelValue?: string;
  sessionId?: string | null;
}>();

const emit = defineEmits<{
  (e: "send", text: string, attachments?: Attachment[]): void;
  (e: "update:modelValue", value: string): void;
}>();

const text = ref(props.modelValue ?? "");
const isModalOpen = ref(false);
const attachments = ref<Attachment[]>([]);
const isUploading = ref(false);
const isDragging = ref(false);
const fileInputRef = ref<HTMLInputElement | null>(null);

const isMultiline = computed(() => {
  return text.value.includes("\n");
});

const lineCount = computed(() => {
  if (!text.value) return 0;
  return text.value.split("\n").length;
});

const canSend = computed(() => {
  if (props.loading || isUploading.value) return false;
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
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
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

const openModal = () => {
  isModalOpen.value = true;
};

const closeModal = () => {
  isModalOpen.value = false;
};

const triggerFileInput = () => {
  if (!props.sessionId || isUploading.value || props.loading) return;
  fileInputRef.value?.click();
};

const processFiles = async (files: FileList | File[]) => {
  if (!props.sessionId || !files || files.length === 0) return;

  isUploading.value = true;
  try {
    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      if (!file) continue;
      try {
        const att = await uploadAttachment(props.sessionId, file);
        if (att) {
          // Avoid duplicate attachment name in list
          if (!attachments.value.some((a) => a.name === att.name)) {
            attachments.value.push(att);
          }
        }
      } catch (err) {
        console.error("Failed to upload attachment:", err);
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
  if (!props.sessionId || !e.clipboardData || !e.clipboardData.files) return;
  const files = e.clipboardData.files;
  if (files.length > 0) {
    e.preventDefault();
    void processFiles(files);
  }
};

const handleDragOver = (e: DragEvent) => {
  if (!props.sessionId || props.loading) return;
  e.preventDefault();
  isDragging.value = true;
};

const handleDragLeave = (e: DragEvent) => {
  e.preventDefault();
  isDragging.value = false;
};

const handleDrop = (e: DragEvent) => {
  if (!props.sessionId || props.loading) return;
  e.preventDefault();
  isDragging.value = false;
  if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
    void processFiles(e.dataTransfer.files);
  }
};
</script>

<template>
  <div
    class="p-2.5 sm:p-4 bg-base-100 border-t border-base-200/50 md:border-t-0 shrink-0 min-w-0 w-full"
    @dragover="handleDragOver"
    @dragleave="handleDragLeave"
    @drop="handleDrop"
    :class="{ 'ring-2 ring-primary ring-inset rounded-lg': isDragging }"
  >
    <!-- Hidden file input -->
    <input ref="fileInputRef" type="file" multiple class="hidden" @change="handleFileChange" />

    <div class="flex flex-col max-w-4xl w-full mx-auto min-w-0 gap-2">
      <!-- Attachment Previews List (Chips) -->
      <div
        v-if="attachments.length > 0 || isUploading"
        class="flex flex-wrap items-center gap-2 px-1"
      >
        <div
          v-for="(att, idx) in attachments"
          :key="att.name + idx"
          class="badge badge-lg gap-1.5 py-3.5 px-3 bg-base-200 border border-base-300 shadow-xs max-w-full text-xs font-mono select-none"
        >
          <Icon :icon="getFileIcon(undefined, att.name)" class="h-4 w-4 shrink-0" />
          <span class="truncate max-w-[160px] sm:max-w-[220px]" :title="att.name">
            {{ att.name }}
          </span>
          <span class="text-base-content/50 text-[11px]"> ({{ formatFileSize(att.size) }}) </span>
          <button
            type="button"
            @click="removeAttachment(idx)"
            class="btn btn-ghost btn-xs btn-circle ml-1 hover:bg-base-300"
            title="Remove attachment"
          >
            <Icon icon="material-symbols:close" class="h-3.5 w-3.5" />
          </button>
        </div>

        <!-- Uploading Indicator Badge -->
        <div
          v-if="isUploading"
          class="badge badge-lg gap-2 py-3.5 px-3 bg-base-200/80 border border-base-300 shadow-xs text-xs font-mono text-base-content/70 select-none"
        >
          <span class="loading loading-spinner loading-xs text-primary"></span>
          <span>Uploading attachment...</span>
        </div>
      </div>

      <div class="relative flex items-center w-full min-w-0">
        <!-- Left Buttons Join/Group (Expand & Attach) -->
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
            title="Expand input editor"
          >
            <Icon icon="iconoir:expand" class="h-4 w-4" />
          </button>

          <!-- Attach File Button (Paperclip) -->
          <button
            v-if="sessionId"
            @click="triggerFileInput"
            type="button"
            :disabled="loading || isUploading"
            class="btn btn-circle btn-sm btn-ghost hover:bg-base-300 text-base-content/70 hover:text-base-content hover:scale-105 active:scale-95 transition-all"
            title="Attach file"
          >
            <Icon icon="material-symbols:attach-file" class="h-4 w-4" />
          </button>
        </div>

        <textarea
          v-model="text"
          @keydown="handleKeyDown"
          @paste="handlePaste"
          :placeholder="`${sendShortcut} to send...`"
          rows="1"
          :disabled="loading"
          class="textarea textarea-bordered bg-base-200 text-base-content w-full rounded-2xl resize-none min-h-[48px] max-h-48 leading-relaxed focus:outline-none focus:border-primary text-base sm:text-sm font-sans placeholder:text-base-content/60"
          :class="sessionId ? 'pl-20 sm:pl-22 pr-11 sm:pr-12' : 'pl-11 sm:pl-12 pr-11 sm:pr-12'"
        ></textarea>

        <!-- Send Button (Right) -->
        <button
          @click="handleSend"
          :disabled="!canSend"
          class="btn btn-circle btn-primary btn-sm absolute right-2.5 sm:right-3 hover:scale-105 active:scale-95 transition-transform"
          :title="`Send message (${sendShortcut})`"
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
            <h3 class="font-bold text-lg">Edit Chat Input</h3>
            <span v-if="lineCount > 1" class="badge badge-sm badge-neutral">
              {{ lineCount }} lines
            </span>
          </div>
          <button
            @click="closeModal"
            class="btn btn-sm btn-circle btn-ghost"
            title="Close modal (Esc)"
          >
            <Icon icon="material-symbols:close" class="h-5 w-5" />
          </button>
        </div>

        <!-- Attachments Preview inside Modal -->
        <div
          v-if="attachments.length > 0 || isUploading"
          class="flex flex-wrap items-center gap-2 mb-3 px-1"
        >
          <div
            v-for="(att, idx) in attachments"
            :key="'modal-' + att.name + idx"
            class="badge badge-lg gap-1.5 py-3.5 px-3 bg-base-200 border border-base-300 shadow-xs max-w-full text-xs font-mono select-none"
          >
            <Icon :icon="getFileIcon(undefined, att.name)" class="h-4 w-4 shrink-0" />
            <span class="truncate max-w-[160px] sm:max-w-[220px]" :title="att.name">
              {{ att.name }}
            </span>
            <span class="text-base-content/50 text-[11px]"> ({{ formatFileSize(att.size) }}) </span>
            <button
              type="button"
              @click="removeAttachment(idx)"
              class="btn btn-ghost btn-xs btn-circle ml-1 hover:bg-base-300"
              title="Remove attachment"
            >
              <Icon icon="material-symbols:close" class="h-3.5 w-3.5" />
            </button>
          </div>

          <div
            v-if="isUploading"
            class="badge badge-lg gap-2 py-3.5 px-3 bg-base-200/80 border border-base-300 shadow-xs text-xs font-mono text-base-content/70 select-none"
          >
            <span class="loading loading-spinner loading-xs text-primary"></span>
            <span>Uploading attachment...</span>
          </div>
        </div>

        <div class="flex-1 min-h-[250px] flex flex-col mb-4">
          <textarea
            v-model="text"
            @keydown="handleKeyDown"
            @paste="handlePaste"
            :placeholder="`Type multiline prompt here... (${sendShortcut} to send)`"
            :disabled="loading"
            class="textarea textarea-bordered bg-base-200 text-base-content w-full flex-1 p-4 rounded-xl leading-relaxed focus:outline-none focus:border-primary text-sm font-mono resize-none"
          ></textarea>
        </div>

        <div class="flex items-center justify-between gap-2 pt-2 border-t border-base-300">
          <div class="flex items-center gap-2">
            <button
              v-if="sessionId"
              @click="triggerFileInput"
              type="button"
              :disabled="loading || isUploading"
              class="btn btn-sm btn-ghost gap-1.5 text-base-content/70 hover:text-base-content"
              title="Attach file"
            >
              <Icon icon="material-symbols:attach-file" class="h-4 w-4" />
              <span class="hidden sm:inline">Attach</span>
            </button>
            <span class="text-xs text-base-content/60 hidden sm:inline">
              Press <kbd class="kbd kbd-xs">{{ modKey }}</kbd> +
              <kbd class="kbd kbd-xs">Enter</kbd> to send
            </span>
          </div>
          <div class="flex items-center gap-2 ml-auto">
            <button @click="closeModal" class="btn btn-sm btn-ghost">Done</button>
            <button @click="handleSend" :disabled="!canSend" class="btn btn-sm btn-primary gap-2">
              <span v-if="loading || isUploading" class="loading loading-spinner loading-xs"></span>
              <Icon v-else icon="material-symbols:send" class="h-4 w-4" />
              Send
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
