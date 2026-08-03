<script setup lang="ts">
import { ref, watch } from "vue";
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
  }
};
</script>

<template>
  <div
    class="p-2.5 sm:p-4 bg-base-100 border-t border-base-200/50 md:border-t-0 shrink-0 min-w-0 w-full"
  >
    <div class="relative flex items-center max-w-4xl w-full mx-auto min-w-0">
      <textarea
        v-model="text"
        @keydown="handleKeyDown"
        placeholder="Ctrl+Enter to send"
        rows="1"
        :disabled="loading"
        class="textarea textarea-bordered bg-base-200 text-base-content w-full pr-12 rounded-2xl resize-none min-h-[48px] max-h-48 leading-relaxed focus:outline-none focus:border-primary text-base sm:text-sm font-sans placeholder:text-base-content/60"
      ></textarea>

      <button
        @click="handleSend"
        :disabled="loading || !text.trim()"
        class="btn btn-circle btn-primary btn-sm absolute right-2.5 sm:right-3 hover:scale-105 active:scale-95 transition-transform"
      >
        <span v-if="loading" class="loading loading-spinner loading-xs"></span>
        <Icon v-else icon="material-symbols:send" class="h-4 w-4 fill-current" />
      </button>
    </div>
  </div>
</template>
