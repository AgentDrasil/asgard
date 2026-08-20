<script setup lang="ts">
import { ref, watch, nextTick, computed } from "vue";
import { Icon } from "@iconify/vue";
import { useCommandPalette } from "../composables/useCommandPalette";
import type { CommandItem } from "../types";

const props = defineProps<{
  isOpen: boolean;
  commands: CommandItem[];
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "execute", command: CommandItem): void;
}>();

const inputRef = ref<HTMLInputElement | null>(null);
const commandsRef = computed(() => props.commands);

const {
  query,
  selectedIndex,
  filteredCommands,
  navigateNext,
  navigatePrevious,
  selectCurrent,
  reset,
} = useCommandPalette(commandsRef);

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

function handleSelect(command: CommandItem) {
  if (!command) return;
  Promise.resolve(command.action()).catch((err) => {
    console.error("Failed to execute command:", err);
  });
  emit("execute", command);
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
        aria-label="Command Palette"
        class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-2xl max-h-[70vh] flex flex-col shadow-2xl overflow-hidden transition-all transform scale-100"
      >
        <!-- Search Input Bar -->
        <div class="p-3 sm:p-4 border-b border-base-100 flex items-center gap-3 bg-base-300/40">
          <Icon icon="material-symbols:terminal" class="h-5 w-5 text-primary shrink-0 ml-1" />
          <input
            ref="inputRef"
            v-model="query"
            type="text"
            placeholder="Type a command or search..."
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
            title="Clear search"
          >
            <Icon icon="mynaui:x" class="h-4 w-4" />
          </button>
        </div>

        <!-- Command Results List / Empty States -->
        <div class="overflow-y-auto flex-1 p-2 divide-y divide-base-100/30">
          <!-- No Results Found State -->
          <div
            v-if="filteredCommands.length === 0"
            class="py-12 px-4 text-center text-base-content/50 text-xs flex flex-col items-center gap-2"
          >
            <Icon icon="octicon:search-24" class="h-8 w-8 text-base-content/20" />
            <span
              >No commands found matching
              <strong class="text-base-content/80">"{{ query }}"</strong></span
            >
          </div>

          <!-- Commands List -->
          <div v-else class="space-y-0.5">
            <div
              v-for="(cmd, index) in filteredCommands"
              :key="cmd.id"
              @click="handleSelect(cmd)"
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
                  v-if="cmd.icon"
                  :icon="cmd.icon"
                  class="h-4 w-4 shrink-0 text-base-content/70"
                />
                <span class="font-medium truncate text-[13px] text-base-content">{{
                  cmd.title
                }}</span>
              </div>

              <div
                class="flex items-center gap-2 shrink-0 text-[11px] text-base-content/50 font-mono"
              >
                <kbd v-if="cmd.shortcut" class="kbd kbd-xs bg-base-100 font-mono">{{
                  cmd.shortcut
                }}</kbd>
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
              <span class="ml-0.5">navigate</span>
            </span>
            <span class="flex items-center gap-1">
              <kbd class="kbd kbd-xs bg-base-100">↵</kbd>
              <span class="ml-0.5">execute</span>
            </span>
            <span class="flex items-center gap-1">
              <kbd class="kbd kbd-xs bg-base-100">esc</kbd>
              <span class="ml-0.5">close</span>
            </span>
          </div>

          <div v-if="filteredCommands.length > 0" class="font-mono">
            {{ filteredCommands.length }} command{{ filteredCommands.length === 1 ? "" : "s" }}
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
