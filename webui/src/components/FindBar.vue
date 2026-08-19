<script setup lang="ts">
import { ref, watch, nextTick, onMounted } from "vue";
import { Icon } from "@iconify/vue";

const props = defineProps<{
  modelValue: string;
  isOpen: boolean;
  currentIndex: number;
  totalMatches: number;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", val: string): void;
  (e: "next"): void;
  (e: "prev"): void;
  (e: "close"): void;
}>();

const inputRef = ref<HTMLInputElement | null>(null);

function focusInput() {
  nextTick(() => {
    inputRef.value?.focus();
    inputRef.value?.select();
  });
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      focusInput();
    }
  },
);

onMounted(() => {
  if (props.isOpen) {
    focusInput();
  }
});
</script>

<template>
  <Transition
    enter-active-class="transition duration-150 ease-out"
    enter-from-class="opacity-0 -translate-y-2 scale-95"
    enter-to-class="opacity-100 translate-y-0 scale-100"
    leave-active-class="transition duration-100 ease-in"
    leave-from-class="opacity-100 translate-y-0 scale-100"
    leave-to-class="opacity-0 -translate-y-2 scale-95"
  >
    <div
      v-if="isOpen"
      class="find-bar-ignore absolute top-3 right-4 z-30 flex items-center gap-1.5 p-1.5 bg-base-200/95 backdrop-blur-md border border-base-300 shadow-2xl rounded-xl text-xs select-none"
      @keydown.stop
    >
      <!-- Search Input Container -->
      <div class="relative flex items-center">
        <input
          ref="inputRef"
          type="text"
          :value="modelValue"
          @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
          @keydown.enter.exact.prevent="emit('next')"
          @keydown.shift.enter.prevent="emit('prev')"
          @keydown.esc.prevent="emit('close')"
          placeholder="Find in page..."
          class="input input-xs input-bordered bg-base-100 pl-2 pr-2 font-mono text-xs text-base-content focus:outline-none focus:border-primary w-36 sm:w-44 h-7 rounded-lg"
        />
      </div>

      <!-- Match Counter -->
      <div class="flex items-center justify-center min-w-[42px] px-1 font-mono text-[11px]">
        <span v-if="totalMatches > 0" class="text-base-content/80 font-medium font-mono">
          {{ currentIndex + 1 }}/{{ totalMatches }}
        </span>
        <span v-else-if="modelValue.trim()" class="text-error font-medium"> 0/0 </span>
        <span v-else class="text-base-content/40"> 0/0 </span>
      </div>

      <!-- Navigation & Close Buttons -->
      <div class="flex items-center gap-0.5 border-l border-base-300 pl-1">
        <!-- Previous Match Button -->
        <button
          @click="emit('prev')"
          :disabled="totalMatches === 0"
          class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content disabled:opacity-30 h-7 w-7 rounded-lg"
          title="Previous match (Shift+Enter)"
          aria-label="Previous match"
        >
          <Icon icon="lucide:chevron-up" class="h-4 w-4" />
        </button>

        <!-- Next Match Button -->
        <button
          @click="emit('next')"
          :disabled="totalMatches === 0"
          class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content disabled:opacity-30 h-7 w-7 rounded-lg"
          title="Next match (Enter)"
          aria-label="Next match"
        >
          <Icon icon="lucide:chevron-down" class="h-4 w-4" />
        </button>

        <!-- Close Button -->
        <button
          @click="emit('close')"
          class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content h-7 w-7 rounded-lg ml-0.5"
          title="Close (Esc)"
          aria-label="Close find bar"
        >
          <Icon icon="lucide:x" class="h-4 w-4" />
        </button>
      </div>
    </div>
  </Transition>
</template>
