<script setup lang="ts">
import { ref } from "vue";
import type { ChatSession } from "../types";
import { Icon } from "@iconify/vue";

const props = defineProps<{
  session: ChatSession;
  isActive: boolean;
}>();

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "delete-session", id: string): void;
}>();

const swipeOffset = ref(0);
const isSwiping = ref(false);
const isOpenSwipe = ref(false);

let startX = 0;
let startY = 0;
let isHorizontalSwipe: boolean | null = null;

const MAX_SWIPE_OFFSET = 44; // Delete button swipe offset width

const handleTouchStart = (e: TouchEvent) => {
  const touch = e.touches[0];
  startX = touch.clientX;
  startY = touch.clientY;
  isSwiping.value = true;
  isHorizontalSwipe = null;
};

const handleTouchMove = (e: TouchEvent) => {
  if (!isSwiping.value) return;
  const touch = e.touches[0];
  const deltaX = touch.clientX - startX;
  const deltaY = touch.clientY - startY;

  // Determine drag direction on initial move
  if (isHorizontalSwipe === null) {
    if (Math.abs(deltaX) > Math.abs(deltaY) && Math.abs(deltaX) > 5) {
      isHorizontalSwipe = true;
    } else if (Math.abs(deltaY) > Math.abs(deltaX) && Math.abs(deltaY) > 5) {
      isHorizontalSwipe = false;
    }
  }

  if (isHorizontalSwipe) {
    // Prevent vertical scrolling while swiping horizontally
    e.preventDefault();

    let offset = isOpenSwipe.value ? -MAX_SWIPE_OFFSET + deltaX : deltaX;
    // Limit range between -MAX_SWIPE_OFFSET and 0 with slight resistance beyond limits
    if (offset > 0) {
      offset = offset * 0.2;
    } else if (offset < -MAX_SWIPE_OFFSET) {
      offset = -MAX_SWIPE_OFFSET + (offset + MAX_SWIPE_OFFSET) * 0.2;
    }
    swipeOffset.value = offset;
  }
};

const handleTouchEnd = () => {
  if (!isSwiping.value) return;
  isSwiping.value = false;

  if (isHorizontalSwipe) {
    // If swiped left beyond threshold, open delete button
    if (swipeOffset.value < -MAX_SWIPE_OFFSET / 2) {
      swipeOffset.value = -MAX_SWIPE_OFFSET;
      isOpenSwipe.value = true;
    } else {
      swipeOffset.value = 0;
      isOpenSwipe.value = false;
    }
  } else if (isOpenSwipe.value && swipeOffset.value > -MAX_SWIPE_OFFSET / 2) {
    swipeOffset.value = 0;
    isOpenSwipe.value = false;
  }
};

const handleClick = () => {
  if (isOpenSwipe.value) {
    // If swipe action menu is open, clicking item closes it instead of selecting session
    swipeOffset.value = 0;
    isOpenSwipe.value = false;
    return;
  }
  emit("select-session", props.session.chatID);
};

const handleDelete = () => {
  swipeOffset.value = 0;
  isOpenSwipe.value = false;
  emit("delete-session", props.session.chatID);
};
</script>

<template>
  <div
    :class="[
      'relative overflow-hidden rounded-lg w-full',
      isActive ? 'shadow-md shadow-primary/10' : '',
    ]"
  >
    <!-- Action / Delete Background Layer -->
    <div
      class="absolute inset-y-0 right-0 flex items-center justify-center bg-error/15 dark:bg-error/25 rounded-lg"
      :style="{ width: `${MAX_SWIPE_OFFSET - 4}px` }"
    >
      <button
        @click.stop="handleDelete"
        class="btn btn-ghost btn-xs text-error hover:bg-error/20 p-1.5 h-8 w-8 rounded-md flex items-center justify-center"
        title="Delete session"
      >
        <Icon icon="mynaui:trash-one" class="h-4 w-4 fill-current text-error" />
      </button>
    </div>

    <!-- Main Content Layer (Swipable) -->
    <div
      @touchstart="handleTouchStart"
      @touchmove="handleTouchMove"
      @touchend="handleTouchEnd"
      @click="handleClick"
      :class="[
        'group flex items-center justify-between px-3 py-2.5 rounded-lg cursor-pointer text-sm font-medium w-full relative gap-2',
        isActive
          ? 'bg-primary text-primary-content'
          : 'bg-base-300 hover:bg-base-200 text-base-content/85',
        isSwiping ? 'transition-none' : 'transition-transform duration-200 ease-out',
      ]"
      :style="{ transform: `translateX(${swipeOffset}px)` }"
    >
      <span class="truncate min-w-0 select-none flex-1">{{
        session.title || "Untitled Chat"
      }}</span>

      <!-- Desktop Mouse Hover Delete Button (Only on devices with cursor/mouse precision) -->
      <button
        @click.stop="handleDelete"
        :class="[
          'btn btn-ghost btn-xs opacity-0 group-hover:opacity-100 transition-opacity p-1 min-h-0 h-6 w-6 shrink-0 hidden [@media(pointer:fine)]:flex items-center justify-center',
          isActive ? 'text-primary-content hover:bg-white/20' : 'text-error hover:bg-error/10',
        ]"
        title="Delete session"
      >
        <Icon icon="mynaui:trash-one" class="h-4 w-4 fill-current" />
      </button>
    </div>
  </div>
</template>
