<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from "vue";
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import TerminalView from "./TerminalView.vue";
import { useResizeHandle } from "../composables/useResizeHandle";
import { useTerminalTheme } from "../composables/useTerminalTheme";
import { useShortcuts } from "../composables/useShortcuts";
import { useRunners } from "../composables/useRunners";

const { t } = useI18n();
const { toggleTerminalShortcut } = useShortcuts();
const { selectedRunnerId } = useRunners();

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

const viewportH = ref(typeof window !== "undefined" ? window.innerHeight : 1000);
const maxHeight = computed(() =>
  Math.max(MIN_HEIGHT, Math.floor(viewportH.value * MAX_HEIGHT_RATIO)),
);
const panelHeight = ref(DEFAULT_HEIGHT);
const onWindowResize = () => {
  viewportH.value = window.innerHeight;
};
watch(maxHeight, (m) => {
  if (panelHeight.value > m) panelHeight.value = m;
});
const isMaximized = ref(false);

// Keep the terminal mounted after its first open so toggling show/hide reuses
// the same ttyd session instead of spawning a new shell each time.
const hasBeenOpened = ref(false);
// The sidebar terminal has no chat and runs on the selected runner, so a runner
// switch must remount (reconnect) it; session terminals follow the chat.
const terminalKey = computed(() =>
  props.terminalType === "sidebar"
    ? `sidebar-${selectedRunnerId.value}`
    : `${props.terminalType}-${props.sessionId}`,
);

// Mobile overlay is a modal dialog: move focus in on open and restore it to
// the opener when hidden. The overlay stays mounted (v-show) so xterm and its
// WebSocket survive hide/show.
const mobileOverlayRef = ref<HTMLElement | null>(null);
const desktopRegionRef = ref<HTMLElement | null>(null);
let opener: HTMLElement | null = null;

function focusMobileOverlay() {
  if (isDesktop.value || !props.isOpen) return;
  const active = document.activeElement;
  if (
    active instanceof HTMLElement &&
    !mobileOverlayRef.value?.contains(active) &&
    !desktopRegionRef.value?.contains(active)
  ) {
    opener = active;
  }
  nextTick(() => {
    const el = mobileOverlayRef.value;
    if (el && !el.contains(document.activeElement)) el.focus();
  });
}

function restoreOpenerFocus() {
  const el = opener;
  opener = null;
  if (el && el.isConnected) el.focus();
}

watch(
  () => props.isOpen,
  (v) => {
    if (v) {
      hasBeenOpened.value = true;
      focusMobileOverlay();
    } else {
      restoreOpenerFocus();
    }
  },
);

// Esc hides the overlay, except while typing in the terminal where Esc belongs
// to the running program (vim, etc.); the back button remains available there.
function onMobileEsc(e: KeyboardEvent) {
  if ((e.target as HTMLElement | null)?.closest?.(".xterm")) return;
  e.stopPropagation();
  emit("hide");
}

// Render only the panel matching the real viewport so a single terminal/WS
// connection is created (ttyd runs with --once = single client).
const isDesktop = ref(
  typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(min-width: 640px)").matches
    : true,
);
let mql: MediaQueryList | null = null;
const onMqlChange = (e: MediaQueryListEvent) => {
  const wasDesktop = isDesktop.value;
  isDesktop.value = e.matches;
  if (!props.isOpen || wasDesktop === e.matches) return;
  if (!e.matches) {
    focusMobileOverlay();
  } else {
    // Overlay (and any focus inside it) is going away while the panel stays
    // open: keep focus in the panel (the opener is restored when it closes).
    nextTick(() => desktopRegionRef.value?.focus());
  }
};

onMounted(() => {
  const savedHeight = localStorage.getItem(STORAGE_KEY);
  if (savedHeight) {
    const parsed = parseInt(savedHeight, 10);
    if (!isNaN(parsed) && parsed >= MIN_HEIGHT) {
      panelHeight.value = Math.min(parsed, maxHeight.value);
    }
  }
  viewportH.value = window.innerHeight;
  window.addEventListener("resize", onWindowResize);

  if (typeof window.matchMedia === "function") {
    mql = window.matchMedia("(min-width: 640px)");
    isDesktop.value = mql.matches;
    mql.addEventListener("change", onMqlChange);
  }
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

const { isResizing, handleProps: resizeHandleProps } = useResizeHandle({
  size: panelHeight,
  min: () => MIN_HEIGHT,
  max: () => maxHeight.value,
  axis: "y",
  direction: -1,
  label: () => t("terminal.resizeLabel"),
  disabled: () => isMaximized.value,
  onCommit: (h) => localStorage.setItem(STORAGE_KEY, h.toString()),
});

const resetHeight = () => {
  const h = Math.min(DEFAULT_HEIGHT, maxHeight.value);
  panelHeight.value = h;
  localStorage.setItem(STORAGE_KEY, h.toString());
};

onUnmounted(() => {
  mql?.removeEventListener("change", onMqlChange);
  window.removeEventListener("resize", onWindowResize);
});
</script>

<template>
  <!-- Desktop Split Panel / Fullscreen Panel -->
  <div
    v-if="hasBeenOpened && isDesktop"
    v-show="isOpen"
    ref="desktopRegionRef"
    tabindex="-1"
    class="focus:outline-none"
    role="region"
    :aria-label="
      terminalType === 'sidebar' ? t('terminal.globalTerminal') : t('terminal.sessionTerminal')
    "
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
        v-bind="resizeHandleProps"
        @dblclick="resetHeight"
        class="h-2 w-full bg-base-300 hover:bg-primary/50 cursor-row-resize flex items-center justify-center group transition-colors shrink-0 z-10"
        :title="t('terminal.dragToResize')"
      >
        <div
          class="w-10 h-1 rounded-full bg-base-content/20 group-hover:bg-primary transition-colors"
        ></div>
      </div>

      <!-- Panel Header Bar -->
      <div
        class="px-3 py-1.5 bg-base-200 border-b border-base-300 flex items-center justify-between shrink-0"
      >
        <div class="flex items-center gap-1.5 text-xs font-semibold text-base-content/80 min-w-0">
          <!-- Mobile Back Button -->
          <button
            @click="emit('hide')"
            class="sm:hidden btn btn-sm btn-ghost btn-square text-base-content/80 hover:text-base-content shrink-0 mr-0.5"
            :title="t('terminal.backToChat')"
          >
            <Icon icon="material-symbols:arrow-back-ios-rounded" class="h-4 w-4 ml-1" />
          </button>
          <Icon icon="mynaui:terminal" class="h-4 w-4 text-primary shrink-0" />
          <span class="truncate">{{
            terminalType === "sidebar"
              ? t("terminal.globalTerminal")
              : t("terminal.sessionTerminal")
          }}</span>
        </div>
        <div class="flex items-center gap-1 shrink-0">
          <!-- Hide / Minimize (collapse, keep terminal alive) -->
          <button
            @click="emit('hide')"
            class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
            :title="t('terminal.hideTerminal', { shortcut: toggleTerminalShortcut })"
          >
            <Icon icon="ep:minus" class="h-3.5 w-3.5" />
          </button>
          <!-- Maximize / Restore Toggle (Desktop only) -->
          <button
            @click="toggleMaximize"
            class="hidden sm:flex btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
            :title="isMaximized ? t('terminal.restoreTerminal') : t('terminal.maximizeTerminal')"
          >
            <Icon
              :icon="isMaximized ? 'octicon:screen-normal-24' : 'octicon:screen-full-24'"
              class="h-3.5 w-3.5"
            />
          </button>
          <!-- Close (destroy terminal) - Desktop only -->
          <button
            @click="closeTerminal"
            class="hidden sm:flex btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
            :title="t('terminal.closeTerminal')"
          >
            <Icon icon="ep:close" class="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      <!-- Terminal -->
      <div class="flex-1 w-full h-full bg-black overflow-hidden relative">
        <div v-if="isResizing" class="absolute inset-0 bg-transparent z-20"></div>
        <TerminalView
          :key="terminalKey"
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
    ref="mobileOverlayRef"
    role="dialog"
    aria-modal="true"
    :aria-label="t('terminal.terminal')"
    tabindex="-1"
    @keydown.esc="onMobileEsc"
    class="fixed inset-0 z-50 bg-base-100 flex flex-col w-full h-[100dvh] overflow-hidden focus:outline-none"
  >
    <header
      class="px-3 py-2.5 bg-base-200 border-b border-base-300 flex items-center justify-between shrink-0"
    >
      <div class="flex items-center gap-2 text-sm font-bold text-base-content min-w-0">
        <!-- Mobile Back Button (Hide/Minimize) -->
        <button
          @click="emit('hide')"
          class="btn btn-sm btn-ghost btn-square text-base-content/80 hover:text-base-content shrink-0"
          :title="t('terminal.backToChatMinimize')"
        >
          <Icon icon="material-symbols:arrow-back-ios-rounded" class="h-4 w-4 ml-1" />
        </button>
        <Icon icon="mynaui:terminal" class="h-5 w-5 text-primary shrink-0" />
        <span class="truncate">{{ t("terminal.terminal") }}</span>
      </div>
      <div class="flex items-center gap-1 shrink-0">
        <!-- Close (destroy terminal) -->
        <button
          @click="closeTerminal"
          class="btn btn-ghost btn-sm btn-square text-base-content/70 hover:text-base-content"
          :title="t('terminal.closeTerminal')"
        >
          <Icon icon="ep:close" class="h-5 w-5" />
        </button>
      </div>
    </header>
    <div class="flex-1 w-full h-full bg-black overflow-hidden">
      <TerminalView
        :key="terminalKey"
        :session-id="sessionId"
        :terminal-type="terminalType"
        :theme="activeTheme"
      />
    </div>
  </div>
</template>
