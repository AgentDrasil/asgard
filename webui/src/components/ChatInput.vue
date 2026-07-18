<script setup lang="ts">
import { ref } from "vue";

const props = defineProps<{
  loading: boolean;
}>();

const emit = defineEmits<{
  (e: "send", text: string): void;
}>();

const text = ref("");

const handleKeyDown = (e: KeyboardEvent) => {
  if (e.key === "Enter" && !e.shiftKey) {
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
  <div class="p-4 bg-base-100 border-t border-base-200">
    <div class="relative flex items-center max-w-4xl w-full mx-auto">
      <textarea
        v-model="text"
        @keydown="handleKeyDown"
        placeholder="Type a message... (Press Enter to send)"
        rows="1"
        :disabled="loading"
        class="textarea textarea-bordered bg-base-200 w-full pr-12 rounded-2xl resize-none min-h-[48px] max-h-48 leading-relaxed focus:outline-none focus:border-primary text-sm font-sans"
      ></textarea>

      <button
        @click="handleSend"
        :disabled="loading || !text.trim()"
        class="btn btn-circle btn-primary btn-sm absolute right-3 hover:scale-105 active:scale-95 transition-transform"
      >
        <span v-if="loading" class="loading loading-spinner loading-xs"></span>
        <svg
          v-else
          xmlns="http://www.w3.org/2000/svg"
          class="h-4 w-4 fill-current"
          viewBox="0 0 24 24"
        >
          <path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z" />
        </svg>
      </button>
    </div>
  </div>
</template>
