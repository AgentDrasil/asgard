<script setup lang="ts">
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import { formatTimestamp } from "../../lib/format";
import { formatRelativeTime } from "../../i18n/timeUtils";
import { getAgentIcon, formatPath } from "../../utils/agentUtils";
import { formatLockTooltip } from "../../utils/dirLockUtils";
import type { ChatSession, AgentInfo } from "../../types";

const props = defineProps<{
  session: ChatSession;
  agents?: AgentInfo[];
  variant?: "running" | "queued" | "waiting" | "recentCompleted" | "completed";
  isUnlocking?: boolean;
  isArchiving?: boolean;
}>();

const emit = defineEmits<{
  (e: "navigate", session: ChatSession): void;
  (e: "unlock", session: ChatSession, ev: Event): void;
  (e: "archive", session: ChatSession, ev: Event): void;
}>();

const { t } = useI18n();
</script>

<template>
  <div
    class="card bg-base-100 border border-base-300 shadow-sm hover:shadow-md transition-all cursor-pointer p-4 group"
    :class="[
      variant === 'running'
        ? 'hover:border-primary/50'
        : variant === 'queued'
          ? 'hover:border-amber-500/50'
          : variant === 'waiting'
            ? 'hover:border-warning/50'
            : variant === 'recentCompleted'
              ? 'hover:border-success/50'
              : 'hover:border-primary/50',
    ]"
    @click="emit('navigate', session)"
    :data-test="
      variant === 'recentCompleted'
        ? 'session-card-recent-completed'
        : variant
          ? `session-card-${variant}`
          : 'session-card'
    "
  >
    <div class="flex items-start justify-between gap-2">
      <h3
        class="font-medium text-sm text-base-content line-clamp-2 flex-1 transition-colors"
        :class="[
          variant === 'running'
            ? 'group-hover:text-primary'
            : variant === 'queued'
              ? 'group-hover:text-amber-500'
              : variant === 'waiting'
                ? 'group-hover:text-warning'
                : variant === 'recentCompleted'
                  ? 'group-hover:text-success'
                  : 'group-hover:text-primary',
        ]"
      >
        {{ session.title || session.chatID }}
      </h3>
      <div class="flex items-center gap-1">
        <!-- Status Badge -->
        <span
          v-if="session.dirLock?.isWaitingForLock"
          class="badge badge-xs badge-warning gap-1 shrink-0"
          :title="formatLockTooltip(session.dirLock, t)"
          :aria-label="formatLockTooltip(session.dirLock, t)"
          data-testid="badge-waiting-lock"
          data-test="badge-waiting-lock"
        >
          <Icon icon="lucide:clock" class="w-3 h-3 animate-pulse" />
          {{ t("dashboard.cards.waitingLockBadge") }}
        </span>
        <span
          v-else-if="variant === 'running'"
          class="badge badge-xs badge-primary animate-pulse shrink-0"
        >
          {{ t("dashboard.cards.runningBadge") }}
        </span>
        <span v-else-if="variant === 'waiting'" class="badge badge-xs badge-warning shrink-0">
          {{ t("dashboard.cards.waitingBadge") }}
        </span>
        <span
          v-else-if="variant === 'recentCompleted'"
          class="badge badge-xs badge-success shrink-0"
        >
          {{ t("dashboard.cards.recentCompletedBadge") }}
        </span>
        <span v-else-if="variant === 'completed'" class="badge badge-xs badge-neutral shrink-0">
          {{ t("dashboard.cards.completedBadge") }}
        </span>

        <!-- Locked Directory Badge -->
        <span
          v-if="session.dirLock?.isLocked"
          :class="[
            'badge badge-xs gap-1 shrink-0',
            session.dirLock?.canUnlock ? 'badge-accent' : 'badge-ghost',
          ]"
          :title="formatLockTooltip(session.dirLock, t)"
          :aria-label="formatLockTooltip(session.dirLock, t)"
          data-testid="badge-locked"
          data-test="badge-locked"
        >
          <Icon icon="lucide:lock" class="w-3 h-3" />
        </span>

        <!-- Unlock Button (if applicable) -->
        <button
          v-if="session.dirLock?.canUnlock"
          type="button"
          class="btn btn-ghost btn-xs btn-square text-accent hover:bg-accent/15"
          :title="t('dirlock.unlockTooltip')"
          :aria-label="t('dirlock.unlockTooltip')"
          :disabled="isUnlocking"
          :aria-busy="isUnlocking"
          @click.stop="emit('unlock', session, $event)"
          data-testid="kanban-unlock-btn"
          data-test="kanban-unlock-btn"
        >
          <Icon v-if="isUnlocking" icon="lucide:loader-2" class="w-3.5 h-3.5 animate-spin" />
          <Icon v-else icon="lucide:lock-open" class="w-3.5 h-3.5" />
        </button>

        <!-- Archive Button -->
        <button
          type="button"
          class="btn btn-ghost btn-xs btn-square opacity-70 hover:opacity-100 hover:text-error"
          :title="t('dashboard.cards.archiveTooltip')"
          :disabled="isArchiving"
          @click.stop="emit('archive', session, $event)"
          data-test="btn-archive"
        >
          <Icon v-if="isArchiving" icon="lucide:loader-2" class="w-3.5 h-3.5 animate-spin" />
          <Icon v-else icon="lucide:archive" class="w-3.5 h-3.5" />
        </button>
      </div>
    </div>

    <!-- Agent & Working Directory -->
    <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-base-content/70">
      <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-base-200 font-mono">
        <Icon
          :icon="getAgentIcon(session.currentAgent, agents, null, session.agentIcon)"
          class="w-3.5 h-3.5 text-primary"
        />
        {{ session.currentAgent || t("dashboard.cards.defaultAgent") }}
      </span>
      <span
        v-if="session.runDir"
        class="inline-flex items-center gap-1 text-base-content/60 truncate max-w-[160px]"
        :title="session.runDir"
      >
        <Icon icon="lucide:folder" class="w-3.5 h-3.5 shrink-0" />
        {{ formatPath(session.runDir) }}
      </span>
    </div>

    <!-- Timestamp & Short Session ID -->
    <div
      class="mt-3 pt-2 border-t border-base-200 flex items-center justify-between text-[11px] text-base-content/50"
    >
      <span :title="formatTimestamp(session.updatedAt || session.createdAt)">
        {{
          variant === "waiting"
            ? t("dashboard.cards.askedTime", {
                time: formatRelativeTime(session.updatedAt || session.createdAt, t),
              })
            : variant === "recentCompleted" || variant === "completed"
              ? t("dashboard.cards.completedTime", {
                  time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                })
              : t("dashboard.cards.activeTime", {
                  time: formatRelativeTime(session.updatedAt || session.createdAt, t),
                })
        }}
      </span>
      <span class="font-mono">{{ session.chatID.slice(0, 8) }}</span>
    </div>
  </div>
</template>
