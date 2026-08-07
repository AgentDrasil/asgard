<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from "vue";
import { Icon } from "@iconify/vue";
import TerminalView from "./TerminalView.vue";
import { useTerminalTheme } from "../composables/useTerminalTheme";

const props = withDefaults(
  defineProps<{
    sessionId: string;
    isOpen: boolean;
    terminalType?: "session" | "sidebar";
  }>(),
  {
    terminalType: "session",
  },
);

const emit = defineEmits<{
  (e: "hide"): void;
  (e: "close"): void;
}>();

const { activeTheme } = useTerminalTheme();

const STORAGE_KEY = "asgard_terminal_panel_height";
const DEFAULT_HEIGHT = 320;
const MIN_HEIGHT = 150;
const MAX_HEIGHT_RATIO = 0.75;

const panelHeight = ref(DEFAULT_HEIGHT);
const isResizing = ref(false);
const isMaximized = ref(false);

let startY = 0;
let startHeight = 0;

// Keep the terminal mounted after its first open so toggling show/hide reuses
// the same ttyd session instead of spawning a new shell each time.
const hasBeenOpened = ref(false);
watch(
  () => props.isOpen,
  (v) => {
    if (v) hasBeenOpened.value = true;
  },
);

// Render only the panel matching the real viewport so a single terminal/WS
// connection is created (ttyd runs with --once = single client).
const isDesktop = ref(
  typeof window !== "undefined" ? window.matchMedia("(min-width: 640px)").matches : true,
);
let mql: MediaQueryList | null = null;
const onMqlChange = (e: MediaQueryListEvent) => {
  isDesktop.value = e.matches;
};

onMounted(() => {
  const savedHeight = localStorage.getItem(STORAGE_KEY);
  if (savedHeight) {
    const parsed = parseInt(savedHeight, 10);
    if (!isNaN(parsed) && parsed >= MIN_HEIGHT) {
      panelHeight.value = parsed;
    }
  }

  mql = window.matchMedia("(min-width: 640px)");
  isDesktop.value = mql.matches;
  mql.addEventListener("change", onMqlChange);
});

const toggleMaximize = () => {
  isMaximized.value = !isMaximized.value;
};

// "Close" really destroys the terminal (unmounts TerminalView -> WS drops ->
// ttyd exits via --once). "Hide" only collapses the panel and keeps the session.
const closeTerminal = () => {
  hasBeenOpened.value = false;
  isMaximized.value = false;
  emit("close");
};

const startResize = (e: MouseEvent | TouchEvent) => {
  if (isMaximized.value) return;
  isResizing.value = true;
  const clientY = "touches" in e ? e.touches[0].clientY : e.clientY;
  startY = clientY;
  startHeight = panelHeight.value;

  window.addEventListener("mousemove", handleResize);
  window.addEventListener("mouseup", stopResize);
  window.addEventListener("touchmove", handleResize);
  window.addEventListener("touchend", stopResize);
};

const handleResize = (e: MouseEvent | TouchEvent) => {
  if (!isResizing.value) return;
  const clientY = "touches" in e ? e.touches[0].clientY : e.clientY;
  const deltaY = startY - clientY; // Dragging up increases height
  const maxHeight = Math.floor(window.innerHeight * MAX_HEIGHT_RATIO);
  let newHeight = startHeight + deltaY;

  if (newHeight < MIN_HEIGHT) newHeight = MIN_HEIGHT;
  if (newHeight > maxHeight) newHeight = maxHeight;

  panelHeight.value = newHeight;
};

const stopResize = () => {
  if (isResizing.value) {
    isResizing.value = false;
    localStorage.setItem(STORAGE_KEY, panelHeight.value.toString());
    window.removeEventListener("mousemove", handleResize);
    window.removeEventListener("mouseup", stopResize);
    window.removeEventListener("touchmove", handleResize);
    window.removeEventListener("touchend", stopResize);
  }
};

const resetHeight = () => {
  panelHeight.value = DEFAULT_HEIGHT;
  localStorage.setItem(STORAGE_KEY, DEFAULT_HEIGHT.toString());
};

onUnmounted(() => {
  stopResize();
  mql?.removeEventListener("change", onMqlChange);
});
</script>

<template>
  <!-- Desktop Split Panel / Fullscreen Panel -->
  <div
    v-if="hasBeenOpened && isDesktop"
    v-show="isOpen"
    :class="[
      isMaximized
        ? 'absolute inset-0 z-40 bg-base-100 flex flex-col w-full h-full overflow-hidden'
        : 'w-full flex flex-col shrink-0 relative select-none',
    ]"
  >
    <div
      class="flex flex-col border-t border-base-300 bg-base-300 w-full relative transition-all duration-75"
      :class="{ 'h-full flex-1': isMaximized }"
      :style="isMaximized ? {} : { height: `${panelHeight}px` }"
    >
      <!-- Resize Handle (only shown when not maximized) -->
      <div
        v-if="!isMaximized"
        @mousedown="startResize"
        @touchstart.passive="startResize"
        @dblclick="resetHeight"
        class="h-2 w-full bg-base-300 hover:bg-primary/50 cursor-row-resize flex items-center justify-center group transition-colors shrink-0 z-10"
        title="Drag to resize, double click to reset"
      >
        <div
          class="w-10 h-1 rounded-full bg-base-content/20 group-hover:bg-primary transition-colors"
        ></div>
      </div>

      <!-- Panel Header Bar -->
      <div
        class="px-3 py-1.5 bg-base-200 border-b border-base-300 flex items-center justify-between shrink-0"
      >
        <div class="flex items-center gap-2 text-xs font-semibold text-base-content/80">
          <Icon icon="mynaui:terminal" class="h-4 w-4 text-primary" />
          <span>{{
            terminalType === "sidebar" ? "Global Terminal" : "Terminal (Agent Workspace)"
          }}</span>
        </div>
        <div class="flex items-center gap-1">
          <!-- Hide (collapse, keep terminal alive) -->
          <button
            @click="emit('hide')"
            class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
            title="Hide Terminal (Ctrl+`)"
          >
            <Icon icon="ep:minus" class="h-3.5 w-3.5" />
          </button>
          <!-- Maximize / Restore Toggle (Full view like diff) -->
          <button
            @click="toggleMaximize"
            class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
            :title="isMaximized ? 'Restore Down' : 'Maximize Terminal'"
          >
            <Icon
              :icon="isMaximized ? 'octicon:screen-normal-24' : 'octicon:screen-full-24'"
              class="h-3.5 w-3.5"
            />
          </button>
          <!-- Close (destroy terminal) -->
          <button
            @click="closeTerminal"
            class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
            title="Close Terminal"
          >
            <Icon icon="ep:close" class="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      <!-- Terminal -->
      <div class="flex-1 w-full h-full bg-black overflow-hidden relative">
        <div v-if="isResizing" class="absolute inset-0 bg-transparent z-20"></div>
        <TerminalView
          :key="`${terminalType}-${sessionId}`"
          :session-id="sessionId"
          :terminal-type="terminalType"
          :theme="activeTheme"
        />
      </div>
    </div>
  </div>

  <!-- Mobile Fullscreen Overlay Panel -->
  <div
    v-if="hasBeenOpened && !isDesktop"
    v-show="isOpen"
    class="fixed inset-0 z-50 bg-base-100 flex flex-col w-full h-[100dvh] overflow-hidden"
  >
    <header
      class="px-3 py-2.5 bg-base-200 border-b border-base-300 flex items-center justify-between shrink-0"
    >
      <div class="flex items-center gap-2 text-sm font-bold text-base-content">
        <Icon icon="mynaui:terminal" class="h-5 w-5 text-primary" />
        <span>Terminal</span>
      </div>
      <div class="flex items-center gap-2">
        <button
          @click="emit('hide')"
          class="btn btn-ghost btn-sm btn-square text-base-content/70"
          title="Hide Terminal"
        >
          <Icon icon="ep:minus" class="h-5 w-5" />
        </button>
        <button
          @click="closeTerminal"
          class="btn btn-ghost btn-sm btn-square text-base-content/70"
          title="Close Terminal"
        >
          <Icon icon="ep:close" class="h-5 w-5" />
        </button>
      </div>
    </header>
    <div class="flex-1 w-full h-full bg-black overflow-hidden">
      <TerminalView
        :key="`${terminalType}-${sessionId}`"
        :session-id="sessionId"
        :terminal-type="terminalType"
        :theme="activeTheme"
      />
    </div>
  </div>
</template>
