<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import type { SupportedOS } from "../types";
import { useShortcuts } from "../composables/useShortcuts";
import { useToast } from "../composables/useToast";
import {
  DEFAULT_KEYBINDING_ACTIONS,
  getDefaultBindingsForOS,
  detectConflicts,
  formatShortcutDisplay,
  normalizeShortcut,
  getActionTitleKey,
  getActionDescriptionKey,
} from "../utils/keybindingUtils";

const router = useRouter();
const { t, te } = useI18n();
const toast = useToast();

const {
  currentOS,
  overrides,
  hasLoadError,
  isLoading,
  loadCustomKeybindings,
  saveCustomKeybindings,
  updateShortcut,
  resetShortcut,
  resetAllShortcuts,
} = useShortcuts();

const selectedOS = ref<SupportedOS>(currentOS.value);
const searchQuery = ref("");
const recordingActionId = ref<string | null>(null);
const recordingKeys = ref<string[]>([]);
const isSaving = ref(false);

const isCurrentOS = computed(() => selectedOS.value === currentOS.value);

// Cancel active recording when switching OS tab
watch(selectedOS, () => {
  stopRecording();
});

// Compute active bindings for the selected OS tab
const selectedOSBindings = computed<Record<string, string[]>>(() => {
  const os = selectedOS.value;
  const defaults = getDefaultBindingsForOS(os);
  const osOverrides = overrides.value[os] || {};

  const merged: Record<string, string[]> = { ...defaults };
  for (const [actionId, rawKeys] of Object.entries(osOverrides)) {
    if (Array.isArray(rawKeys)) {
      merged[actionId] = rawKeys.map(normalizeShortcut).filter(Boolean);
    } else if (typeof rawKeys === "string") {
      const norm = normalizeShortcut(rawKeys);
      merged[actionId] = norm ? [norm] : [];
    } else {
      merged[actionId] = [];
    }
  }
  return merged;
});

// Conflicts for selected OS
const conflictsMap = computed(() => detectConflicts(selectedOSBindings.value));

// Filtered and grouped actions
const categories = [
  { id: "navigation", label: "Navigation" },
  { id: "panel", label: "Panels" },
  { id: "chat", label: "Chat" },
  { id: "general", label: "General" },
] as const;

const getActionTitle = (actionId: string, fallbackTitle: string): string => {
  const key = getActionTitleKey(actionId);
  return te(key) ? t(key) : fallbackTitle;
};

const getActionDescription = (actionId: string, fallbackDesc: string): string => {
  const key = getActionDescriptionKey(actionId);
  return te(key) ? t(key) : fallbackDesc;
};

const getCategoryLabel = (catId: string, fallbackLabel: string): string => {
  const key = `keybindings.categories.${catId}`;
  return te(key) ? t(key) : fallbackLabel;
};

const filteredActions = computed(() => {
  const query = searchQuery.value.trim().toLowerCase();
  if (!query) return DEFAULT_KEYBINDING_ACTIONS;

  return DEFAULT_KEYBINDING_ACTIONS.filter((action) => {
    const title = getActionTitle(action.id, action.title);
    const desc = getActionDescription(action.id, action.description);
    return (
      title.toLowerCase().includes(query) ||
      desc.toLowerCase().includes(query) ||
      action.title.toLowerCase().includes(query) ||
      action.description.toLowerCase().includes(query) ||
      action.id.toLowerCase().includes(query)
    );
  });
});

const groupedActions = computed(() => {
  const result: Record<string, typeof DEFAULT_KEYBINDING_ACTIONS> = {};
  for (const cat of categories) {
    result[cat.id] = filteredActions.value.filter((a) => a.category === cat.id);
  }
  return result;
});

const getActionConflicts = (actionId: string): string[] => {
  const currentKeys = selectedOSBindings.value[actionId] || [];
  const conflictingActionNames: string[] = [];

  for (const key of currentKeys) {
    const norm = normalizeShortcut(key);
    if (!norm) continue;
    const conflictActionIds = conflictsMap.value.get(norm);
    if (conflictActionIds && conflictActionIds.length > 1) {
      for (const otherId of conflictActionIds) {
        if (otherId !== actionId) {
          const actionDef = DEFAULT_KEYBINDING_ACTIONS.find((a) => a.id === otherId);
          const name = actionDef ? getActionTitle(actionDef.id, actionDef.title) : otherId;
          if (!conflictingActionNames.includes(name)) {
            conflictingActionNames.push(name);
          }
        }
      }
    }
  }
  return conflictingActionNames;
};

const isActionModified = (actionId: string): boolean => {
  const osOverrides = overrides.value[selectedOS.value];
  return Boolean(osOverrides && actionId in osOverrides);
};

// Recorder logic
const startRecording = (actionId: string) => {
  if (!isCurrentOS.value) return;
  recordingActionId.value = actionId;
  recordingKeys.value = [];
};

const stopRecording = () => {
  recordingActionId.value = null;
  recordingKeys.value = [];
};

const handleKeyDown = (event: KeyboardEvent) => {
  if (!recordingActionId.value || !isCurrentOS.value) return;

  event.preventDefault();
  event.stopPropagation();

  if (event.key === "Escape") {
    stopRecording();
    return;
  }

  // Check if the pressed key is purely a modifier
  const isModifierOnly = ["Control", "Meta", "Alt", "Shift", "OS"].includes(event.key);
  if (isModifierOnly) {
    return;
  }

  // Construct shortcut tokens
  const parts: string[] = [];
  if (event.ctrlKey) parts.push("Ctrl");
  if (event.metaKey) parts.push("Cmd");
  if (event.altKey) parts.push("Alt");
  if (event.shiftKey) parts.push("Shift");

  // Determine key part
  let keyPart = event.key;
  if (event.code === "Backquote") {
    keyPart = "Backquote";
  } else if (/^F([1-9]|1[0-2])$/i.test(event.key)) {
    keyPart = event.key.toUpperCase();
  } else if (event.code.startsWith("Key") && event.code.length === 4) {
    keyPart = event.code.slice(3).toUpperCase();
  } else if (event.code.startsWith("Digit") && event.code.length === 6) {
    keyPart = event.code.slice(5);
  } else if (event.key === " ") {
    keyPart = "Space";
  }

  parts.push(keyPart);
  const recordedShortcut = normalizeShortcut(parts.join("+"));

  if (recordedShortcut) {
    updateShortcut(recordingActionId.value, recordedShortcut, selectedOS.value);
  }
  stopRecording();
};

const handleClear = (actionId: string) => {
  if (!isCurrentOS.value) return;
  updateShortcut(actionId, [], selectedOS.value);
};

const handleResetAction = (actionId: string) => {
  if (!isCurrentOS.value) return;
  resetShortcut(actionId, selectedOS.value);
};

const handleResetAll = () => {
  if (!isCurrentOS.value) return;
  resetAllShortcuts(selectedOS.value);
  toast.info(t("keybindings.resetToast"), { title: t("keybindings.resetToastTitle") });
};

const handleSave = async () => {
  if (hasLoadError.value || isSaving.value || !isCurrentOS.value) return;

  isSaving.value = true;
  try {
    const res = await saveCustomKeybindings(selectedOS.value);
    if (res.success) {
      toast.success(t("keybindings.savedToast"), { title: t("keybindings.savedToastTitle") });
    } else {
      toast.error(res.error || t("keybindings.saveFailedToast"), {
        title: t("keybindings.saveFailedToastTitle"),
      });
    }
  } catch (err: any) {
    toast.error(err?.message || t("keybindings.saveFailedToast"), {
      title: t("keybindings.saveFailedToastTitle"),
    });
  } finally {
    isSaving.value = false;
  }
};

const handleBack = () => {
  if (window.history.state?.back) {
    router.back();
  } else {
    router.push("/settings");
  }
};

onMounted(() => {
  void loadCustomKeybindings();
  window.addEventListener("keydown", handleKeyDown, { capture: true });
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleKeyDown, { capture: true });
});
</script>

<template>
  <div class="flex flex-col h-full w-full bg-base-100 overflow-y-auto">
    <!-- Header -->
    <header
      class="sticky top-0 z-20 flex items-center justify-between border-b border-base-300 bg-base-100/90 px-4 py-3 backdrop-blur md:px-6"
    >
      <div class="flex items-center gap-3">
        <button
          @click="handleBack"
          class="btn btn-ghost btn-sm btn-square"
          :title="t('keybindings.backToSettings')"
          :aria-label="t('keybindings.backToSettings')"
        >
          <Icon icon="material-symbols:arrow-back" class="w-5 h-5" />
        </button>
        <div class="flex items-center gap-2">
          <Icon icon="material-symbols:keyboard-outline" class="w-5 h-5 text-primary" />
          <h1 class="text-base font-semibold md:text-lg">{{ t("keybindings.title") }}</h1>
        </div>
      </div>

      <!-- Action Search in Header -->
      <div class="w-48 sm:w-64">
        <label class="input input-sm input-bordered flex items-center gap-2">
          <Icon icon="material-symbols:search" class="w-4 h-4 text-base-content/50" />
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="t('keybindings.searchPlaceholder')"
            class="grow text-xs"
            :aria-label="t('keybindings.searchAria')"
          />
        </label>
      </div>
    </header>

    <!-- Main Content -->
    <div class="p-4 md:p-6 max-w-5xl w-full mx-auto space-y-6 flex-1">
      <!-- Corrupted Config Warning Banner (R-2) -->
      <div v-if="hasLoadError" class="alert alert-error shadow-sm text-sm" role="alert">
        <Icon icon="material-symbols:error-outline" class="w-5 h-5 shrink-0" />
        <div>
          <h3 class="font-bold">{{ t("keybindings.configLoadErrorTitle") }}</h3>
          <div class="text-xs">
            {{ t("keybindings.configLoadErrorDesc") }}
          </div>
        </div>
      </div>

      <!-- OS Selector Tabs -->
      <div
        class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 border-b border-base-300 pb-3"
      >
        <div class="tabs tabs-boxed bg-base-200 p-1">
          <button
            class="tab text-xs sm:text-sm font-medium gap-1.5"
            :class="{ 'tab-active': selectedOS === 'linux' }"
            @click="selectedOS = 'linux'"
          >
            <span>Linux</span>
            <span v-if="currentOS === 'linux'" class="badge badge-primary badge-xs">
              {{ t("keybindings.currentOS") }}
            </span>
          </button>
          <button
            class="tab text-xs sm:text-sm font-medium gap-1.5"
            :class="{ 'tab-active': selectedOS === 'windows' }"
            @click="selectedOS = 'windows'"
          >
            <span>Windows</span>
            <span v-if="currentOS === 'windows'" class="badge badge-primary badge-xs">
              {{ t("keybindings.currentOS") }}
            </span>
          </button>
          <button
            class="tab text-xs sm:text-sm font-medium gap-1.5"
            :class="{ 'tab-active': selectedOS === 'mac' }"
            @click="selectedOS = 'mac'"
          >
            <span>macOS</span>
            <span v-if="currentOS === 'mac'" class="badge badge-primary badge-xs">
              {{ t("keybindings.currentOS") }}
            </span>
          </button>
        </div>

        <div class="text-xs text-base-content/60">
          {{
            t("keybindings.showingFor", {
              os: selectedOS === "mac" ? "macOS" : selectedOS === "linux" ? "Linux" : "Windows",
            })
          }}
        </div>
      </div>

      <!-- Non-current OS Read-only Notice Banner -->
      <div v-if="!isCurrentOS" class="alert alert-warning shadow-sm text-xs py-2 px-3">
        <Icon icon="material-symbols:info-outline" class="w-4 h-4 shrink-0" />
        <span>
          {{
            t("keybindings.viewOnlyNotice", {
              os: currentOS === "mac" ? "macOS" : currentOS === "linux" ? "Linux" : "Windows",
            })
          }}
        </span>
      </div>

      <!-- Keybindings Action Lists Grouped by Category -->
      <div class="space-y-6">
        <template v-for="cat in categories" :key="cat.id">
          <section v-if="groupedActions[cat.id]?.length" class="space-y-3">
            <h2
              class="text-sm font-semibold uppercase tracking-wider text-base-content/60 flex items-center gap-2"
            >
              <span>{{ getCategoryLabel(cat.id, cat.label) }}</span>
              <span class="text-xs font-normal text-base-content/40"
                >({{ groupedActions[cat.id].length }})</span
              >
            </h2>

            <div
              class="divide-y divide-base-300 rounded-xl border border-base-300 bg-base-200/40 overflow-hidden"
            >
              <div
                v-for="action in groupedActions[cat.id]"
                :key="action.id"
                class="p-3.5 sm:p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-3 hover:bg-base-200/70 transition-colors"
                :class="{ 'bg-primary/5': recordingActionId === action.id }"
              >
                <!-- Left: Title, Description, Badges -->
                <div class="space-y-1 min-w-0 flex-1">
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="font-medium text-sm text-base-content">{{
                      getActionTitle(action.id, action.title)
                    }}</span>
                    <span
                      v-if="isActionModified(action.id)"
                      class="badge badge-outline badge-info badge-xs"
                    >
                      {{ t("keybindings.modified") }}
                    </span>
                  </div>
                  <p class="text-xs text-base-content/70 leading-relaxed">
                    {{ getActionDescription(action.id, action.description) }}
                  </p>

                  <!-- Conflict Warning if any -->
                  <div
                    v-if="getActionConflicts(action.id).length > 0"
                    class="flex items-center gap-1.5 text-xs text-error font-medium mt-1"
                  >
                    <Icon icon="material-symbols:warning" class="w-4 h-4 shrink-0" />
                    <span>{{
                      t("keybindings.conflictsWith", {
                        actions: getActionConflicts(action.id).join(", "),
                      })
                    }}</span>
                  </div>
                </div>

                <!-- Right: Shortcut Badge & Controls -->
                <div class="flex items-center gap-2 shrink-0 self-end sm:self-center">
                  <!-- Shortcut Badges -->
                  <div class="flex items-center gap-1">
                    <kbd
                      v-if="recordingActionId === action.id"
                      class="kbd kbd-sm bg-primary text-primary-content animate-pulse"
                    >
                      {{ t("keybindings.pressKeys") }}
                    </kbd>
                    <template v-else>
                      <template v-if="(selectedOSBindings[action.id] || []).length > 0">
                        <kbd
                          v-for="(key, idx) in selectedOSBindings[action.id]"
                          :key="idx"
                          class="kbd kbd-sm font-mono text-xs shadow-xs"
                        >
                          {{ formatShortcutDisplay(key, selectedOS) }}
                        </kbd>
                      </template>
                      <span v-else class="text-xs italic text-base-content/40 px-2">
                        {{ t("keybindings.unassigned") }}
                      </span>
                    </template>
                  </div>

                  <!-- Controls (Record / Clear / Reset) -->
                  <div v-if="recordingActionId !== action.id" class="flex items-center gap-1 ml-2">
                    <button
                      class="btn btn-ghost btn-xs"
                      :title="t('keybindings.recordTitle')"
                      :disabled="!isCurrentOS"
                      @click="startRecording(action.id)"
                    >
                      <Icon icon="material-symbols:edit-outline" class="w-3.5 h-3.5" />
                      <span class="hidden md:inline">{{ t("keybindings.record") }}</span>
                    </button>

                    <button
                      class="btn btn-ghost btn-xs text-base-content/70 hover:text-error"
                      :title="t('keybindings.clearTitle')"
                      :disabled="!isCurrentOS || (selectedOSBindings[action.id] || []).length === 0"
                      @click="handleClear(action.id)"
                    >
                      <Icon icon="material-symbols:backspace-outline" class="w-3.5 h-3.5" />
                    </button>

                    <button
                      v-if="isActionModified(action.id)"
                      class="btn btn-ghost btn-xs text-base-content/70 hover:text-primary"
                      :title="t('keybindings.resetTitle')"
                      :disabled="!isCurrentOS"
                      @click="handleResetAction(action.id)"
                    >
                      <Icon icon="material-symbols:restore" class="w-3.5 h-3.5" />
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </section>
        </template>

        <div
          v-if="filteredActions.length === 0"
          class="text-center py-12 text-base-content/50 space-y-2"
        >
          <Icon icon="material-symbols:search-off" class="w-8 h-8 mx-auto text-base-content/30" />
          <p class="text-sm">{{ t("keybindings.noMatch", { query: searchQuery }) }}</p>
        </div>
      </div>
    </div>

    <!-- Bottom Action Bar -->
    <footer
      class="sticky bottom-0 z-20 border-t border-base-300 bg-base-100/90 px-4 py-3 backdrop-blur md:px-6 flex items-center justify-between"
    >
      <button
        class="btn btn-ghost btn-sm text-base-content/70 hover:text-error gap-1.5"
        :disabled="!isCurrentOS || isLoading"
        @click="handleResetAll"
      >
        <Icon icon="material-symbols:restart-alt" class="w-4 h-4" />
        <span>{{ t("keybindings.resetAll") }}</span>
      </button>

      <div class="flex items-center gap-3">
        <button
          class="btn btn-primary btn-sm gap-2"
          :disabled="!isCurrentOS || hasLoadError || isSaving || isLoading"
          @click="handleSave"
        >
          <Icon
            icon="material-symbols:save"
            class="w-4 h-4"
            :class="{ 'animate-spin': isSaving }"
          />
          <span>{{ isSaving ? t("keybindings.saving") : t("keybindings.saveChanges") }}</span>
        </button>
      </div>
    </footer>
  </div>
</template>
