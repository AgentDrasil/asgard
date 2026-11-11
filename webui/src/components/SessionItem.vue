<script setup lang="ts">
import { ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import type { ChatSession } from "../types";
import { Icon } from "@iconify/vue";
import { formatLockTooltip } from "../utils/dirLockUtils";

const { t } = useI18n();

const props = withDefaults(
  defineProps<{
    session: ChatSession;
    isActive: boolean;
    /** Nesting level under a parent chat (0 for a top-level chat). */
    depth?: number;
    /** Number of subagent chats nested directly under this one. */
    childCount?: number;
    /** The subagents of this chat are folded away. */
    collapsed?: boolean;
  }>(),
  { depth: 0, childCount: 0, collapsed: false },
);

const isSubagent = computed(() => !!props.session.parentChatId);
const displayTitle = computed(() =>
  isSubagent.value
    ? props.session.spawnLabel || props.session.title || t("subagents.untitled")
    : props.session.title || t("sidebar.untitledChat"),
);
const stateBadgeClass = computed(() => {
  switch (props.session.subagentState) {
    case "running":
      return "badge-info";
    case "queued":
      return "badge-warning";
    case "failed":
      return "badge-error";
    case "killed":
      return "badge-neutral";
    default:
      return "badge-success";
  }
});

const lockTooltip = computed(() => {
  return formatLockTooltip(props.session.dirLock, t);
});

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "delete-session", id: string): void;
  (e: "archive-session", id: string): void;
  (e: "toggle-children", id: string): void;
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

const handleArchive = () => {
  emit("archive-session", props.session.chatID);
};
</script>

<template>
  <div
    :class="[
      'relative overflow-hidden rounded-lg w-full',
      isActive ? 'shadow-md shadow-primary/10' : '',
    ]"
    :style="depth > 0 ? { paddingLeft: `${Math.min(depth, 4) * 12}px` } : undefined"
    :data-depth="depth"
  >
    <!-- Action / Delete Background Layer -->
    <div
      class="absolute inset-y-0 right-0 flex items-center justify-center bg-error/15 dark:bg-error/25 rounded-lg"
      :style="{ width: `${MAX_SWIPE_OFFSET - 4}px` }"
    >
      <button
        @click.stop="handleDelete"
        class="btn btn-ghost btn-xs text-error hover:bg-error/20 p-1.5 h-8 w-8 rounded-md flex items-center justify-center"
        :title="t('sidebar.deleteSession')"
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
      <!-- Directory Lock Indicator / Badge -->
      <span
        v-if="session.dirLock?.isWaitingForLock"
        class="badge badge-warning badge-xs gap-1 shrink-0 px-1.5 py-0.5"
        :title="lockTooltip"
        :aria-label="lockTooltip"
        data-testid="session-waiting-lock-badge"
      >
        <Icon icon="lucide:clock" class="h-3 w-3 shrink-0" />
        <span class="text-[10px] hidden sm:inline">{{ t("dirlock.queuedBadge") }}</span>
      </span>
      <span
        v-else-if="session.dirLock?.isLocked"
        :class="[
          'badge badge-xs gap-1 shrink-0 px-1.5 py-0.5',
          session.dirLock?.canUnlock ? 'badge-accent' : 'badge-ghost',
        ]"
        :title="lockTooltip"
        :aria-label="lockTooltip"
        data-testid="session-locked-badge"
      >
        <Icon icon="lucide:lock" class="h-3 w-3 shrink-0" />
        <span v-if="session.dirLock?.canUnlock" class="text-[10px] hidden sm:inline">{{
          t("dirlock.uncommittedBadge")
        }}</span>
      </span>
      <button
        v-if="childCount > 0"
        type="button"
        class="shrink-0 flex items-center justify-center h-5 w-5 rounded hover:bg-black/10"
        :title="collapsed ? t('subagents.expand') : t('subagents.collapse')"
        :aria-label="collapsed ? t('subagents.expand') : t('subagents.collapse')"
        :aria-expanded="!collapsed"
        data-testid="session-toggle-children"
        @click.stop="emit('toggle-children', session.chatID)"
      >
        <Icon
          icon="mynaui:chevron-down"
          :class="['h-3.5 w-3.5 fill-current transition-transform', collapsed ? '-rotate-90' : '']"
        />
        <span class="text-[10px] ml-0.5">{{ childCount }}</span>
      </button>
      <Icon
        v-if="isSubagent"
        icon="lucide:git-fork"
        class="h-3.5 w-3.5 shrink-0 opacity-70"
        aria-hidden="true"
      />
      <span class="truncate min-w-0 select-none flex-1" data-testid="session-title">{{
        displayTitle
      }}</span>

      <!-- Subagent details: agent, access mode and state -->
      <div v-if="isSubagent" class="flex items-center gap-1 shrink-0" data-testid="subagent-badges">
        <span
          class="badge badge-ghost badge-xs max-w-[72px] truncate hidden sm:inline-flex"
          :title="session.currentAgent"
          >{{ session.currentAgent }}</span
        >
        <span class="badge badge-outline badge-xs" data-testid="subagent-mode">{{
          session.spawnReadonly ? t("subagents.readonly") : t("subagents.writable")
        }}</span>
        <span
          v-if="session.subagentState"
          :class="['badge badge-xs', stateBadgeClass]"
          data-testid="subagent-state"
          >{{ t(`subagents.state.${session.subagentState}`) }}</span
        >
      </div>

      <div class="flex items-center gap-1 shrink-0">
        <!-- Desktop Mouse Hover Archive Button -->
        <button
          @click.stop="handleArchive"
          :class="[
            'btn btn-ghost btn-xs opacity-0 group-hover:opacity-100 transition-opacity p-1 min-h-0 h-6 w-6 hidden [@media(pointer:fine)]:flex items-center justify-center',
            isActive
              ? 'text-primary-content hover:bg-white/20'
              : 'text-base-content/70 hover:text-base-content hover:bg-base-100',
          ]"
          :title="t('sidebar.archiveSession')"
        >
          <Icon icon="mynaui:archive" class="h-4 w-4 fill-current" />
        </button>

        <!-- Desktop Mouse Hover Delete Button (Only on devices with cursor/mouse precision) -->
        <button
          @click.stop="handleDelete"
          :class="[
            'btn btn-ghost btn-xs opacity-0 group-hover:opacity-100 transition-opacity p-1 min-h-0 h-6 w-6 hidden [@media(pointer:fine)]:flex items-center justify-center',
            isActive ? 'text-primary-content hover:bg-white/20' : 'text-error hover:bg-error/10',
          ]"
          :title="t('sidebar.deleteSession')"
        >
          <Icon icon="mynaui:trash-one" class="h-4 w-4 fill-current" />
        </button>
      </div>
    </div>
  </div>
</template>
