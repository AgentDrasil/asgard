<script setup lang="ts">
import { ref, watch, computed } from "vue";
import { Icon } from "@iconify/vue";

const props = defineProps<{
  loading: boolean;
  modelValue?: string;
}>();

const emit = defineEmits<{
  (e: "send", text: string): void;
  (e: "update:modelValue", value: string): void;
}>();

const text = ref(props.modelValue ?? "");
const isModalOpen = ref(false);

const isMultiline = computed(() => {
  return text.value.includes("\n");
});

const lineCount = computed(() => {
  if (!text.value) return 0;
  return text.value.split("\n").length;
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
  if (text.value.trim() && !props.loading) {
    emit("send", text.value.trim());
    text.value = "";
    isModalOpen.value = false;
  }
};

const openModal = () => {
  isModalOpen.value = true;
};

const closeModal = () => {
  isModalOpen.value = false;
};
</script>

<template>
  <div
    class="p-2.5 sm:p-4 bg-base-100 border-t border-base-200/50 md:border-t-0 shrink-0 min-w-0 w-full"
  >
    <div class="relative flex items-center max-w-4xl w-full mx-auto min-w-0">
      <!-- Expand Button (Left) -->
      <button
        @click="openModal"
        type="button"
        class="btn btn-circle btn-sm absolute left-2.5 sm:left-3 z-10 hover:scale-105 active:scale-95 transition-all shadow-sm"
        :class="
          isMultiline
            ? 'btn-success'
            : 'btn-neutral bg-base-300 text-base-content hover:bg-base-300/80 border-none'
        "
        title="Expand input editor"
      >
        <Icon icon="iconoir:expand" class="h-4 w-4" />
      </button>

      <textarea
        v-model="text"
        @keydown="handleKeyDown"
        placeholder="Ctrl+Enter to send..."
        rows="1"
        :disabled="loading"
        class="textarea textarea-bordered bg-base-200 text-base-content w-full pl-11 sm:pl-12 pr-11 sm:pr-12 rounded-2xl resize-none min-h-[48px] max-h-48 leading-relaxed focus:outline-none focus:border-primary text-base sm:text-sm font-sans placeholder:text-base-content/60"
      ></textarea>

      <!-- Send Button (Right) -->
      <button
        @click="handleSend"
        :disabled="loading || !text.trim()"
        class="btn btn-circle btn-primary btn-sm absolute right-2.5 sm:right-3 hover:scale-105 active:scale-95 transition-transform"
        title="Send message (Ctrl+Enter)"
      >
        <span v-if="loading" class="loading loading-spinner loading-xs"></span>
        <Icon v-else icon="material-symbols:send" class="h-4 w-4 fill-current" />
      </button>
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

        <div class="flex-1 min-h-[250px] flex flex-col mb-4">
          <textarea
            v-model="text"
            @keydown="handleKeyDown"
            placeholder="Type multiline prompt here... (Ctrl+Enter to send)"
            :disabled="loading"
            class="textarea textarea-bordered bg-base-200 text-base-content w-full flex-1 p-4 rounded-xl leading-relaxed focus:outline-none focus:border-primary text-sm font-mono resize-none"
          ></textarea>
        </div>

        <div class="flex items-center justify-between gap-2 pt-2 border-t border-base-300">
          <span class="text-xs text-base-content/60 hidden sm:inline">
            Press <kbd class="kbd kbd-xs">Ctrl</kbd> + <kbd class="kbd kbd-xs">Enter</kbd> to send
          </span>
          <div class="flex items-center gap-2 ml-auto">
            <button @click="closeModal" class="btn btn-sm btn-ghost">Done</button>
            <button
              @click="handleSend"
              :disabled="loading || !text.trim()"
              class="btn btn-sm btn-primary gap-2"
            >
              <span v-if="loading" class="loading loading-spinner loading-xs"></span>
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
