<script setup lang="ts">
import { ref } from "vue";
import { Icon } from "@iconify/vue";

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
  <div class="p-4 bg-base-100">
    <div class="relative flex items-center max-w-4xl w-full mx-auto">
      <textarea
        v-model="text"
        @keydown="handleKeyDown"
        placeholder="Type a message... (Press Enter to send)"
        rows="1"
        :disabled="loading"
        class="textarea textarea-bordered bg-base-200 text-base-content w-full pr-12 rounded-2xl resize-none min-h-[48px] max-h-48 leading-relaxed focus:outline-none focus:border-primary text-sm font-sans placeholder:text-base-content/60"
      ></textarea>

      <button
        @click="handleSend"
        :disabled="loading || !text.trim()"
        class="btn btn-circle btn-primary btn-sm absolute right-3 hover:scale-105 active:scale-95 transition-transform"
      >
        <span v-if="loading" class="loading loading-spinner loading-xs"></span>
        <Icon v-else icon="material-symbols:send" class="h-4 w-4 fill-current" />
      </button>
    </div>
  </div>
</template>
