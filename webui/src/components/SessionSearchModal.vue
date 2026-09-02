<script setup lang="ts">
import { ref, watch, nextTick } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { useSessionSearchState } from "../composables/useSessionSearchState";
import type { ChatSession } from "../types";

const { t } = useI18n();

const props = defineProps<{
  isOpen: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "select-session", session: ChatSession): void;
}>();

const inputRef = ref<HTMLInputElement | null>(null);

const {
  query,
  results,
  selectedIndex,
  isLoading,
  errorMessage,
  navigateNext,
  navigatePrevious,
  selectCurrent,
  reset,
} = useSessionSearchState();

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      reset();
      nextTick(() => {
        inputRef.value?.focus();
      });
    } else {
      reset();
    }
  },
  { immediate: true },
);

function handleSelect(session: ChatSession) {
  emit("select-session", session);
  emit("close");
}

function handleSelectCurrent() {
  const current = selectCurrent();
  if (current) {
    handleSelect(current);
  }
}

function handleClear() {
  query.value = "";
  inputRef.value?.focus();
}

function getDisplayTitle(session: ChatSession): string {
  const title = session.title?.trim();
  if (title) return title;
  return session.chatID.length > 8 ? session.chatID.slice(0, 8) + "…" : session.chatID;
}

function getDirName(path?: string): string {
  return path?.trim() || "";
}
</script>

<template>
  <Transition name="fade">
    <div
      v-if="isOpen"
      class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-start justify-center pt-20 sm:pt-28 p-4"
      @click.self="emit('close')"
      @keydown.esc.prevent="emit('close')"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Search Sessions"
        class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-2xl max-h-[70vh] flex flex-col shadow-2xl overflow-hidden transition-all transform scale-100"
      >
        <!-- Search Input Bar -->
        <div class="p-3 sm:p-4 border-b border-base-100 flex items-center gap-3 bg-base-300/40">
          <Icon icon="material-symbols:search" class="h-5 w-5 text-primary shrink-0 ml-1" />
          <input
            ref="inputRef"
            v-model="query"
            type="text"
            :placeholder="t('sessionSearch.placeholder')"
            class="input input-ghost w-full focus:outline-none focus:bg-transparent text-sm sm:text-base text-base-content placeholder:text-base-content/40 px-0 h-9"
            @keydown.down.prevent="navigateNext"
            @keydown.up.prevent="navigatePrevious"
            @keydown.enter.prevent="handleSelectCurrent"
            @keydown.esc.prevent="emit('close')"
          />
          <button
            v-if="query"
            @click="handleClear"
            class="btn btn-ghost btn-xs btn-circle text-base-content/60 hover:text-base-content"
            :title="t('sessionSearch.clearSearch')"
          >
            <Icon icon="mynaui:x" class="h-4 w-4" />
          </button>
          <div v-if="isLoading" class="shrink-0 flex items-center pr-1">
            <span class="loading loading-spinner loading-xs text-primary"></span>
          </div>
        </div>

        <!-- Search Results List / Empty States -->
        <div class="overflow-y-auto flex-1 p-2 divide-y divide-base-100/30">
          <!-- Error State -->
          <div v-if="errorMessage" class="p-4 text-center">
            <div class="alert alert-error text-xs">
              <Icon icon="mynaui:danger" class="h-4 w-4 shrink-0" />
              <span>{{ errorMessage }}</span>
            </div>
          </div>

          <!-- Empty Query State -->
          <div
            v-else-if="!query.trim() && results.length === 0 && !isLoading"
            class="py-12 px-4 text-center text-base-content/40 text-xs flex flex-col items-center gap-2"
          >
            <Icon icon="octicon:comment-discussion-24" class="h-8 w-8 text-base-content/20" />
            <span>{{ t("sessionSearch.typeToSearch") }}</span>
          </div>

          <!-- No Results Found State -->
          <div
            v-else-if="query.trim() && results.length === 0 && !isLoading"
            class="py-12 px-4 text-center text-base-content/50 text-xs flex flex-col items-center gap-2"
          >
            <Icon icon="octicon:search-24" class="h-8 w-8 text-base-content/20" />
            <span>{{ t("sessionSearch.noResults", { query }) }}</span>
          </div>

          <!-- Results List -->
          <div v-else class="space-y-0.5">
            <div
              v-for="(session, index) in results"
              :key="session.chatID"
              @click="handleSelect(session)"
              @mouseenter="selectedIndex = index"
              :class="[
                'flex items-center justify-between px-3 py-2 rounded-lg cursor-pointer transition-colors text-xs select-none gap-3',
                index === selectedIndex
                  ? 'bg-primary/15 text-primary'
                  : 'hover:bg-base-300/60 text-base-content/80',
              ]"
            >
              <div class="flex items-center gap-2.5 min-w-0 flex-1">
                <Icon
                  icon="material-symbols:chat-outline"
                  class="h-4 w-4 shrink-0 text-base-content/70"
                />
                <div class="flex flex-col min-w-0">
                  <div class="flex items-center gap-1.5 min-w-0">
                    <span class="font-medium truncate text-[13px] text-base-content">{{
                      getDisplayTitle(session)
                    }}</span>
                    <span
                      class="badge badge-xs text-primary text-[10px] px-1 py-0 h-4 font-mono shrink-0"
                    >
                      {{ session.currentAgent }}
                    </span>
                    <span
                      v-if="session.isRunning"
                      class="badge badge-xs badge-success text-success text-[10px] px-1 py-0 h-4 font-mono shrink-0"
                    >
                      {{ t("sessionSearch.running") }}
                    </span>
                  </div>
                  <span
                    v-if="getDirName(session.runDir)"
                    class="text-[11px] text-base-content/50 font-mono truncate"
                  >
                    {{ getDirName(session.runDir) }}
                  </span>
                </div>
              </div>

              <div
                class="flex items-center gap-2 shrink-0 text-[11px] text-base-content/50 font-mono"
              >
                <Icon
                  v-if="index === selectedIndex"
                  icon="material-symbols:keyboard-return"
                  class="h-3.5 w-3.5 text-primary opacity-80"
                />
              </div>
            </div>
          </div>
        </div>

        <!-- Palette Footer / Keyboard Shortcuts Hints -->
        <div
          class="px-4 py-2 border-t border-base-100 flex items-center justify-between bg-base-300/30 text-[11px] text-base-content/50"
        >
          <div class="flex items-center gap-3">
            <span class="flex items-center gap-1">
              <kbd class="kbd kbd-xs bg-base-100">↑</kbd>
              <kbd class="kbd kbd-xs bg-base-100">↓</kbd>
              <span class="ml-0.5">{{ t("sessionSearch.navigate") }}</span>
            </span>
            <span class="flex items-center gap-1">
              <kbd class="kbd kbd-xs bg-base-100">↵</kbd>
              <span class="ml-0.5">{{ t("sessionSearch.select") }}</span>
            </span>
            <span class="flex items-center gap-1">
              <kbd class="kbd kbd-xs bg-base-100">esc</kbd>
              <span class="ml-0.5">{{ t("sessionSearch.close") }}</span>
            </span>
          </div>

          <div v-if="results.length > 0" class="font-mono">
            {{ t("sessionSearch.sessionsCount", { count: results.length }) }}
          </div>
        </div>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.15s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
