<script setup lang="ts">
import { ref, watch, nextTick } from "vue";
import { Icon } from "@iconify/vue";
import type { QueuedMessage } from "../../types";

const props = defineProps<{
  message: QueuedMessage;
  index: number;
  total?: number;
}>();

const emit = defineEmits<{
  (e: "edit", id: string, text: string): void;
  (e: "delete", id: string): void;
}>();

const isEditing = ref(false);
const editText = ref(props.message.prompt);
const textareaRef = ref<HTMLTextAreaElement | null>(null);

watch(
  () => props.message.prompt,
  (newVal) => {
    if (!isEditing.value) {
      editText.value = newVal;
    }
  },
);

const startEdit = async () => {
  editText.value = props.message.prompt;
  isEditing.value = true;
  await nextTick();
  textareaRef.value?.focus();
};

const cancelEdit = () => {
  isEditing.value = false;
  editText.value = props.message.prompt;
};

const saveEdit = () => {
  const trimmed = editText.value.trim();
  if (!trimmed) return;
  emit("edit", props.message.id, trimmed);
  isEditing.value = false;
};

const handleDelete = () => {
  emit("delete", props.message.id);
};
</script>

<template>
  <div
    class="card bg-base-200/70 hover:bg-base-200 border border-dashed border-base-300 rounded-2xl shadow-xs transition-all p-3.5 sm:p-4 w-full"
    data-testid="queued-message-card"
  >
    <!-- Card Header -->
    <div class="flex items-center justify-between gap-2 mb-2">
      <div class="flex items-center gap-2">
        <span
          class="badge badge-warning badge-sm sm:badge-md gap-1 font-semibold shadow-xs"
          data-testid="queued-badge"
        >
          <Icon icon="material-symbols:schedule" class="w-3.5 h-3.5 animate-pulse" />
          {{ $t("chat.queuedBadge", { index: index + 1 }) }}
        </span>
        <span class="text-xs text-base-content/60 hidden sm:inline">
          {{ $t("chat.queueCardTip") }}
        </span>
      </div>

      <!-- Action Buttons (when not editing) -->
      <div v-if="!isEditing" class="flex items-center gap-1">
        <button
          type="button"
          @click="startEdit"
          class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
          :title="$t('chat.editQueued')"
          data-testid="edit-queued-button"
        >
          <Icon icon="material-symbols:edit-outline" class="w-4 h-4" />
        </button>
        <button
          type="button"
          @click="handleDelete"
          class="btn btn-ghost btn-xs btn-square text-error/70 hover:text-error hover:bg-error/10"
          :title="$t('chat.deleteQueued')"
          data-testid="delete-queued-button"
        >
          <Icon icon="material-symbols:delete-outline" class="w-4 h-4" />
        </button>
      </div>
    </div>

    <!-- Card Content / Edit Mode -->
    <div v-if="isEditing" class="flex flex-col gap-2 mt-1">
      <textarea
        ref="textareaRef"
        v-model="editText"
        rows="2"
        class="textarea textarea-bordered bg-base-100 text-base-content w-full rounded-xl resize-none text-sm leading-relaxed focus:outline-none focus:border-primary"
        data-testid="queued-edit-textarea"
        @keydown.enter.ctrl.prevent="saveEdit"
        @keydown.enter.meta.prevent="saveEdit"
        @keydown.esc.prevent="cancelEdit"
      ></textarea>
      <div class="flex items-center justify-end gap-2">
        <button
          type="button"
          @click="cancelEdit"
          class="btn btn-xs btn-ghost"
          data-testid="cancel-edit-button"
        >
          {{ $t("common.cancel") }}
        </button>
        <button
          type="button"
          @click="saveEdit"
          :disabled="!editText.trim()"
          class="btn btn-xs btn-primary gap-1"
          data-testid="save-edit-button"
        >
          <Icon icon="material-symbols:check" class="w-3.5 h-3.5" />
          {{ $t("common.save") }}
        </button>
      </div>
    </div>

    <!-- Readonly Display Mode -->
    <div
      v-else
      class="text-sm text-base-content/90 whitespace-pre-wrap break-words leading-relaxed pl-1"
      data-testid="queued-prompt-content"
    >
      {{ message.prompt }}
    </div>
  </div>
</template>
