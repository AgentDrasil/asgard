<script setup lang="ts">
import { computed, ref } from "vue";
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import type { ChatSession } from "../../types";
import { subagentLabel, subagentStateBadgeClass } from "../../utils/sessionTree";

const { t } = useI18n();

const props = defineProps<{
  subagents: ChatSession[];
  /** Ids with a kill request in flight. */
  killingIds?: string[];
}>();

const emit = defineEmits<{
  (e: "open", id: string): void;
  (e: "kill", id: string): void;
}>();

const collapsed = ref(false);

const liveCount = computed(
  () =>
    props.subagents.filter((s) => s.subagentState === "running" || s.subagentState === "queued")
      .length,
);

const isLive = (s: ChatSession) => s.subagentState === "running" || s.subagentState === "queued";

const stateClass = (s: ChatSession) => subagentStateBadgeClass(s.subagentState);
</script>

<template>
  <div
    v-if="subagents.length > 0"
    class="border-b border-base-300 bg-base-200/60 px-3 sm:px-6 py-1.5 shrink-0"
    data-testid="subagents-panel"
  >
    <button
      type="button"
      class="flex items-center gap-1.5 text-xs font-semibold text-base-content/70 select-none w-full text-left"
      :aria-expanded="!collapsed"
      data-testid="subagents-panel-toggle"
      @click="collapsed = !collapsed"
    >
      <Icon
        icon="mynaui:chevron-down"
        :class="['h-3.5 w-3.5 fill-current transition-transform', collapsed ? '-rotate-90' : '']"
      />
      <Icon icon="lucide:git-fork" class="h-3.5 w-3.5" />
      <span>{{ t("subagents.title") }}</span>
      <span class="font-normal text-base-content/50">
        ({{ subagents.length }}<template v-if="liveCount > 0">, {{ liveCount }} </template
        ><template v-if="liveCount > 0">{{ t("subagents.state.running") }}</template>)
      </span>
    </button>
    <ul v-if="!collapsed" class="mt-1 space-y-1 max-h-40 overflow-y-auto">
      <li
        v-for="s in subagents"
        :key="s.chatID"
        class="flex items-center gap-2 text-xs min-w-0"
        data-testid="subagent-row"
      >
        <span
          :class="['badge badge-xs gap-1 shrink-0 font-medium', stateClass(s)]"
          data-testid="subagent-row-state"
          ><span
            v-if="s.subagentState === 'running'"
            class="w-1.5 h-1.5 rounded-full bg-current animate-pulse"
            aria-hidden="true"
          />{{ s.subagentState ? t(`subagents.state.${s.subagentState}`) : "" }}</span
        >
        <span
          class="truncate font-medium min-w-0"
          :title="subagentLabel(s, t('subagents.untitled'))"
          >{{ subagentLabel(s, t("subagents.untitled")) }}</span
        >
        <span class="badge badge-outline badge-xs shrink-0 opacity-75">{{
          s.spawnReadonly ? t("subagents.readonly") : t("subagents.writable")
        }}</span>
        <span
          v-if="s.currentAgent"
          class="text-base-content/60 truncate max-w-[140px] sm:max-w-[200px] hidden sm:inline"
          :title="s.currentAgent"
          >{{ s.currentAgent }}</span
        >
        <span class="ml-auto flex items-center gap-1 shrink-0">
          <button
            type="button"
            class="btn btn-ghost btn-xs h-6 min-h-0 px-1.5"
            :title="t('subagents.open')"
            :aria-label="t('subagents.open')"
            data-testid="subagent-open"
            @click="emit('open', s.chatID)"
          >
            <Icon icon="lucide:external-link" class="h-3.5 w-3.5" />
          </button>
          <button
            v-if="isLive(s)"
            type="button"
            class="btn btn-ghost btn-xs h-6 min-h-0 px-1.5 text-error"
            :disabled="killingIds?.includes(s.chatID)"
            :title="t('subagents.kill')"
            :aria-label="t('subagents.kill')"
            data-testid="subagent-kill"
            @click="emit('kill', s.chatID)"
          >
            <Icon icon="material-symbols:stop-circle-rounded" class="h-3.5 w-3.5" />
          </button>
        </span>
      </li>
    </ul>
  </div>
</template>
