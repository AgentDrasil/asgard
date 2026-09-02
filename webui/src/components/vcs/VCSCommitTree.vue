<script setup lang="ts">
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import type { GitCommit } from "../../types";

const { t } = useI18n();

defineProps<{
  commits: GitCommit[];
  unstashedCount: number;
  selectedCommit: string | null;
  loading?: boolean;
}>();

const emit = defineEmits<{
  (e: "select-commit", hash: string | null): void;
}>();

function parseRefBadges(refs?: string): { name: string; isHead: boolean; isTag: boolean }[] {
  if (!refs) return [];
  return refs
    .split(",")
    .map((r) => r.trim())
    .filter(Boolean)
    .map((r) => {
      const isHead = r.includes("HEAD");
      const isTag = r.startsWith("tag:");
      const cleanName = r.replace(/^tag:\s*/, "").replace(/HEAD\s*->\s*/, "");
      return { name: cleanName, isHead, isTag };
    });
}
</script>

<template>
  <div class="flex flex-col h-full overflow-hidden min-h-0 bg-base-100">
    <!-- Header -->
    <div
      class="px-3 py-2 bg-base-200/50 border-b border-base-300 flex items-center justify-between shrink-0"
    >
      <div class="flex items-center gap-1.5 text-xs font-bold text-base-content/80">
        <Icon icon="octicon:git-commit-24" class="h-4 w-4 text-primary" />
        <span>{{ t("vcs.commits") }}</span>
        <span class="badge badge-xs badge-neutral text-[10px] font-mono">{{ commits.length }}</span>
      </div>
      <span v-if="loading" class="loading loading-spinner loading-xs text-primary"></span>
    </div>

    <!-- Commits Tree List -->
    <div class="flex-1 overflow-y-auto overflow-x-hidden p-2 space-y-1 relative">
      <!-- 1. Top Node: Unstash (Working Directory / Uncommitted changes) -->
      <div class="relative pl-6">
        <!-- Tree Track Vertical Line -->
        <div class="absolute left-2.5 top-3 bottom-0 w-0.5 bg-base-300"></div>

        <!-- Node Dot -->
        <div
          :class="[
            'absolute left-1 top-2.5 w-3.5 h-3.5 rounded-full border-2 flex items-center justify-center transition-colors z-10',
            selectedCommit === null
              ? 'border-primary bg-primary text-primary-content shadow-xs'
              : unstashedCount > 0
                ? 'border-warning bg-warning/20'
                : 'border-base-content/30 bg-base-100',
          ]"
        >
          <div
            v-if="selectedCommit === null"
            class="w-1.5 h-1.5 rounded-full bg-primary-content"
          ></div>
        </div>

        <button
          @click="emit('select-commit', null)"
          :class="[
            'w-full text-left p-2 rounded-lg text-xs transition-colors cursor-pointer border',
            selectedCommit === null
              ? 'bg-primary/10 border-primary/40 shadow-xs'
              : 'border-transparent hover:bg-base-200/80 hover:border-base-300',
          ]"
        >
          <div class="flex items-center justify-between gap-1.5">
            <div class="flex items-center gap-1.5 min-w-0">
              <Icon
                icon="material-symbols:edit-document-outline-rounded"
                class="h-3.5 w-3.5 text-warning shrink-0"
              />
              <span class="font-bold text-base-content truncate">{{ t("vcs.unstash") }}</span>
            </div>
            <span
              v-if="unstashedCount > 0"
              class="badge badge-xs badge-warning text-[10px] font-mono shrink-0"
            >
              {{ t("vcs.changedCount", { count: unstashedCount }) }}
            </span>
            <span v-else class="text-[10px] text-base-content/40 font-mono shrink-0">
              {{ t("vcs.clean") }}
            </span>
          </div>
          <p class="text-[11px] text-base-content/60 truncate mt-0.5">{{ t("vcs.unstashDesc") }}</p>
        </button>
      </div>

      <!-- 2. Commits List (up to 10 commits) -->
      <div v-for="(commit, idx) in commits" :key="commit.hash" class="relative pl-6">
        <!-- Tree Track Vertical Line -->
        <div
          v-if="idx < commits.length - 1"
          class="absolute left-2.5 top-0 bottom-0 w-0.5 bg-base-300"
        ></div>
        <div v-else class="absolute left-2.5 top-0 h-3.5 w-0.5 bg-base-300"></div>

        <!-- Node Dot -->
        <div
          :class="[
            'absolute left-1 top-2.5 w-3.5 h-3.5 rounded-full border-2 flex items-center justify-center transition-colors z-10 bg-base-100',
            selectedCommit === commit.hash
              ? 'border-primary bg-primary shadow-xs'
              : 'border-base-content/40 hover:border-primary/80',
          ]"
        >
          <div
            v-if="selectedCommit === commit.hash"
            class="w-1.5 h-1.5 rounded-full bg-primary-content"
          ></div>
        </div>

        <button
          @click="emit('select-commit', commit.hash)"
          :class="[
            'w-full text-left p-2 rounded-lg text-xs transition-colors cursor-pointer border group',
            selectedCommit === commit.hash
              ? 'bg-primary/10 border-primary/40 shadow-xs'
              : 'border-transparent hover:bg-base-200/80 hover:border-base-300',
          ]"
        >
          <!-- Commit Message -->
          <div class="flex items-start justify-between gap-1">
            <span
              class="font-medium text-base-content line-clamp-2 leading-tight group-hover:text-primary transition-colors"
              :title="commit.message"
            >
              {{ commit.message }}
            </span>
          </div>

          <!-- Ref Badges (Branch/Tags) -->
          <div
            v-if="parseRefBadges(commit.refs).length > 0"
            class="flex flex-wrap items-center gap-1 mt-1"
          >
            <span
              v-for="b in parseRefBadges(commit.refs)"
              :key="b.name"
              :class="[
                'badge badge-xs text-[10px] truncate max-w-[120px]',
                b.isHead ? 'badge-primary' : b.isTag ? 'badge-secondary' : 'badge-ghost',
              ]"
              :title="b.name"
            >
              {{ b.name }}
            </span>
          </div>

          <!-- Commit Meta: Short Hash + Author + Date -->
          <div
            class="flex items-center justify-between text-[10px] text-base-content/50 font-mono mt-1 gap-1"
          >
            <span class="bg-base-300/80 px-1 py-0.2 rounded text-base-content/70">
              {{ commit.shortHash }}
            </span>
            <span class="truncate max-w-[100px]">{{ commit.author }}</span>
            <span class="shrink-0">{{ commit.relativeDate }}</span>
          </div>
        </button>
      </div>
    </div>
  </div>
</template>
