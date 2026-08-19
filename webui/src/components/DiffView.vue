<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from "vue";
import { DiffView, DiffModeEnum, SplitSide } from "@git-diff-view/vue";
import { DiffFile } from "@git-diff-view/vue";
import "@git-diff-view/vue/styles/diff-view.css";
import { Icon } from "@iconify/vue";
import { getGitDiff, getGitLog } from "../lib/api";
import VCSSidebar from "./vcs/VCSSidebar.vue";
import type { GitDiffFile, GitCommit, CommentEntry } from "../types";
import { useShortcuts } from "../composables/useShortcuts";
import { commentKey, rebuildChatInputFromComments } from "../utils/commentUtils";

const {
  toggleDiffShortcut,
  toggleTerminalShortcut,
  toggleArtifactsShortcut,
  toggleFileViewShortcut,
} = useShortcuts();

const props = defineProps<{
  runDir: string;
  gitRoot: string;
  chatInputText: string;
  isTerminalOpen?: boolean;
}>();

const isVCSSidebarOpen = defineModel<boolean>("isVCSSidebarOpen", { default: true });

const emit = defineEmits<{
  (e: "close"): void;
  (e: "open-file-view"): void;
  (e: "update:chatInputText", val: string): void;
  (e: "toggle-terminal"): void;
}>();

// ── Git data ────────────────────────────────────────────────────────────────
const files = ref<GitDiffFile[]>([]);
const commits = ref<GitCommit[]>([]);
const currentBranch = ref("main");
const trackingBranch = ref("");
const ahead = ref(0);
const behind = ref(0);
const unstashedCount = ref(0);
const selectedCommit = ref<string | null>(null); // null represents "unstash" (working copy)

const loading = ref(false);
const errorMsg = ref("");
const selectedIndex = ref(0);

const selectedFile = computed(() => files.value[selectedIndex.value] ?? null);

const diffFileObj = computed((): DiffFile | null => {
  const f = selectedFile.value;
  if (!f) return null;
  const df = DiffFile.createInstance({
    oldFile: { fileName: f.oldPath, content: f.oldContent },
    newFile: { fileName: f.newPath, content: f.newContent },
    hunks: f.hunks,
  });
  df.initTheme(theme.value);
  df.init();
  df.buildSplitDiffLines();
  df.buildUnifiedDiffLines();
  return df;
});

// ── VCS Sidebar width management (Independent from Chat Artifacts) ──────────
const DEFAULT_VCS_WIDTH = 380;
const MIN_VCS_WIDTH = 260;
const MAX_VCS_WIDTH = 800;

const vcsSidebarWidth = ref(DEFAULT_VCS_WIDTH);
const isResizingSidebar = ref(false);
const isDesktop = ref(typeof window !== "undefined" && window.innerWidth >= 768);
const mobileActiveTab = ref<"files" | "diff">("diff");

const updateWindowWidth = () => {
  isDesktop.value = window.innerWidth >= 768;
};

const startSidebarResize = (e: MouseEvent) => {
  e.preventDefault();
  isResizingSidebar.value = true;
  document.addEventListener("mousemove", handleSidebarMouseMove);
  document.addEventListener("mouseup", stopSidebarResize);
  document.body.style.userSelect = "none";
  document.body.style.cursor = "col-resize";
};

const handleSidebarMouseMove = (e: MouseEvent) => {
  if (!isResizingSidebar.value) return;
  const newWidth = Math.min(Math.max(window.innerWidth - e.clientX, MIN_VCS_WIDTH), MAX_VCS_WIDTH);
  vcsSidebarWidth.value = newWidth;
};

const stopSidebarResize = () => {
  if (isResizingSidebar.value) {
    isResizingSidebar.value = false;
    localStorage.setItem("asgard_vcs_sidebar_width", vcsSidebarWidth.value.toString());
    document.removeEventListener("mousemove", handleSidebarMouseMove);
    document.removeEventListener("mouseup", stopSidebarResize);
    document.body.style.userSelect = "";
    document.body.style.cursor = "";
  }
};

async function loadGitData() {
  if (!props.runDir) return;
  loading.value = true;
  errorMsg.value = "";
  try {
    // 1. Fetch git log metadata (commits, branch, ahead/behind, unstashed count)
    const logData = await getGitLog(props.runDir);
    if (logData) {
      commits.value = logData.commits || [];
      currentBranch.value = logData.currentBranch || "main";
      trackingBranch.value = logData.trackingBranch || "";
      ahead.value = logData.ahead || 0;
      behind.value = logData.behind || 0;
      unstashedCount.value = logData.unstashedCount || 0;
    }

    // 2. Fetch diff for current selection (selectedCommit or unstash)
    await loadDiffOnly();
  } catch (e: any) {
    errorMsg.value = e?.message ?? "Failed to load git data";
  } finally {
    loading.value = false;
  }
}

async function loadDiffOnly() {
  activeWidget.value = null;
  try {
    const result = await getGitDiff(props.runDir, selectedCommit.value || undefined);
    files.value = result;
    if (selectedIndex.value >= result.length) {
      selectedIndex.value = 0;
    }
  } catch (e: any) {
    errorMsg.value = e?.message ?? "Failed to load diff";
  }
}

async function handleSelectCommit(hash: string | null) {
  selectedCommit.value = hash;
  selectedIndex.value = 0;
  await loadDiffOnly();
  if (!isDesktop.value) {
    mobileActiveTab.value = "diff";
  }
}

function handleSelectFile(index: number) {
  selectedIndex.value = index;
  activeWidget.value = null;
  if (!isDesktop.value) {
    mobileActiveTab.value = "diff";
  }
}

onMounted(() => {
  window.addEventListener("resize", updateWindowWidth);
  const savedWidth = localStorage.getItem("asgard_vcs_sidebar_width");
  if (savedWidth) {
    const parsed = parseInt(savedWidth, 10);
    if (!isNaN(parsed) && parsed >= MIN_VCS_WIDTH && parsed <= MAX_VCS_WIDTH) {
      vcsSidebarWidth.value = parsed;
    }
  }
  loadGitData();
});

onUnmounted(() => {
  window.removeEventListener("resize", updateWindowWidth);
});

watch(() => props.runDir, loadGitData);

// ── View mode ────────────────────────────────────────────────────────────────
// Default: Split on desktop, Unified on mobile
const isMobile = typeof window !== "undefined" && window.innerWidth < 768;
const viewMode = ref<DiffModeEnum>(isMobile ? DiffModeEnum.Unified : DiffModeEnum.Split);

// ── Theme ────────────────────────────────────────────────────────────────────
const theme = ref<"dark" | "light">((localStorage.getItem("theme") as "dark" | "light") ?? "dark");

const syncTheme = () => {
  const docTheme = document.documentElement.getAttribute("data-theme");
  theme.value = docTheme === "light" ? "light" : "dark";
};

const observer = new MutationObserver(syncTheme);
onMounted(() => {
  syncTheme();
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
});
onUnmounted(() => observer.disconnect());

// ── Line comments (in-memory) ────────────────────────────────────────────────
const comments = ref<Map<string, CommentEntry>>(new Map());

interface ActiveWidget {
  side: SplitSide;
  lineNumber: number;
}
const activeWidget = ref<ActiveWidget | null>(null);
const widgetInput = ref("");

// Called by DiffView via @on-add-widget-click
function handleAddWidgetClick(lineNumber: number, side: SplitSide) {
  const key = commentKey(selectedFile.value?.newPath ?? "", lineNumber, sideName(side));
  const existing = comments.value.get(key);
  widgetInput.value = existing?.comment ?? "";
  if (activeWidget.value?.side === side && activeWidget.value?.lineNumber === lineNumber) {
    // Toggle off if clicking the same line again
    activeWidget.value = null;
    widgetInput.value = "";
  } else {
    activeWidget.value = { side, lineNumber };
  }
}

function closeWidget() {
  activeWidget.value = null;
  widgetInput.value = "";
}

function getLineContent(side: SplitSide, lineNumber: number): string {
  const f = selectedFile.value;
  if (!f) return "";
  const source = side === SplitSide.old ? f.oldContent : f.newContent;
  const lines = source.split("\n");
  return lines[lineNumber - 1] ?? "";
}

function sideName(side: SplitSide): "old" | "new" {
  return side === SplitSide.old ? "old" : "new";
}

function submitComment() {
  if (!activeWidget.value || !selectedFile.value) return;
  const { side, lineNumber } = activeWidget.value;
  const filePath = selectedFile.value.newPath;
  const key = commentKey(filePath, lineNumber, sideName(side));
  const lineContent = getLineContent(side, lineNumber);

  if (!widgetInput.value.trim()) {
    comments.value.delete(key);
  } else {
    comments.value.set(key, {
      filePath,
      side: sideName(side),
      lineNumber,
      lineContent,
      comment: widgetInput.value.trim(),
    });
  }

  rebuildChatInput();
  closeWidget();
}

function deleteComment(key: string) {
  comments.value.delete(key);
  rebuildChatInput();
}

function rebuildChatInput() {
  emit("update:chatInputText", rebuildChatInputFromComments(comments.value));
}

function hasComment(side: SplitSide, lineNumber: number): boolean {
  const filePath = selectedFile.value?.newPath ?? "";
  return comments.value.has(commentKey(filePath, lineNumber, sideName(side)));
}

const commentedFileList = computed(() => {
  return Array.from(comments.value.values()).map((c) => c.filePath);
});
</script>

<template>
  <div class="flex-1 flex flex-col h-full overflow-hidden bg-base-100 min-w-0">
    <!-- Header -->
    <header
      class="px-3 py-2 sm:px-4 sm:py-2.5 bg-base-200 border-b border-base-300 flex items-center justify-between gap-2 shrink-0 shadow-sm"
    >
      <!-- Left: Branch Name & Refresh Button -->
      <div class="flex items-center gap-1.5 min-w-0">
        <div class="flex items-center gap-1.5 font-mono text-xs truncate">
          <Icon icon="octicon:git-branch-24" class="h-4 w-4 text-primary shrink-0" />
          <span class="font-bold text-base-content truncate">{{ currentBranch }}</span>
          <span
            v-if="selectedCommit"
            class="badge badge-xs badge-primary font-mono text-[10px] truncate"
          >
            {{ selectedCommit.substring(0, 7) }}
          </span>
          <span
            v-else
            class="badge badge-xs badge-outline text-[10px] truncate hidden sm:inline-flex"
          >
            Unstash
          </span>

          <!-- Refresh Button right next to branch name -->
          <button
            @click="loadGitData"
            :disabled="loading"
            class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content shrink-0 ml-0.5"
            title="Refresh Git Diff & Log"
          >
            <Icon icon="mynaui:refresh" :class="['h-3.5 w-3.5', { 'animate-spin': loading }]" />
          </button>
        </div>
      </div>

      <!-- Center: Mobile Tab Segmented Switcher / Desktop Diff Mode -->
      <div class="flex items-center gap-2">
        <!-- Mobile Segmented Tab Switcher -->
        <div class="md:hidden join bg-base-300/70 p-0.5 rounded-lg">
          <button
            @click="mobileActiveTab = 'files'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              mobileActiveTab === 'files'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70',
            ]"
          >
            <Icon icon="octicon:file-diff-24" class="h-3 w-3" />
            <span>Files</span>
            <span class="badge badge-xs text-[9px] bg-base-100/30 text-current">{{
              files.length
            }}</span>
          </button>
          <button
            @click="mobileActiveTab = 'diff'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1 text-[11px]',
              mobileActiveTab === 'diff'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70',
            ]"
          >
            <Icon icon="material-symbols:difference-outline" class="h-3 w-3" />
            <span>Diff</span>
          </button>
        </div>

        <!-- Desktop Mode toggle (Split / Unified) -->
        <div class="hidden md:flex items-center">
          <div class="join bg-base-300/60 p-0.5 rounded-lg">
            <button
              @click="viewMode = DiffModeEnum.Split"
              :class="[
                'join-item btn btn-xs border-none font-medium gap-1',
                viewMode === DiffModeEnum.Split
                  ? 'btn-primary shadow-xs'
                  : 'btn-ghost text-base-content/70 hover:text-base-content',
              ]"
              title="Side by Side"
            >
              <Icon icon="material-symbols:view-column-2-outline" class="h-3.5 w-3.5" />
              Split
            </button>
            <button
              @click="viewMode = DiffModeEnum.Unified"
              :class="[
                'join-item btn btn-xs border-none font-medium gap-1',
                viewMode === DiffModeEnum.Unified
                  ? 'btn-primary shadow-xs'
                  : 'btn-ghost text-base-content/70 hover:text-base-content',
              ]"
              title="Unified"
            >
              <Icon icon="material-symbols:view-stream-outline" class="h-3.5 w-3.5" />
              Unified
            </button>
          </div>
        </div>
      </div>

      <!-- Right: View Switcher Join Group & Layout Controls Join Group -->
      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
        <!-- View Switcher Join Group (Chat / VCS / Files) - consistent with Chat View -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <button
            @click="emit('close')"
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-ghost text-base-content/70 hover:text-base-content"
            :title="`Switch to Chat View (${toggleDiffShortcut})`"
          >
            <Icon icon="material-symbols:chat-outline" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">Chat</span>
          </button>
          <button
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-primary shadow-xs"
            title="VCS View"
          >
            <Icon icon="octicon:git-branch-24" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">VCS</span>
          </button>
          <button
            @click="emit('open-file-view')"
            class="join-item btn btn-xs border-none font-medium gap-1 sm:gap-1.5 btn-ghost text-base-content/70 hover:text-base-content"
            :title="`Switch to File View (${toggleFileViewShortcut})`"
          >
            <Icon icon="octicon:file-code-24" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">Files</span>
          </button>
        </div>

        <!-- Layout Controls Join Group (Bottom Panel / Right Sidebar) -->
        <div class="join bg-base-300/60 p-0.5 rounded-lg shrink-0">
          <!-- Toggle Terminal Bottom Panel Button (VS Code style) -->
          <button
            @click="emit('toggle-terminal')"
            class="join-item btn btn-xs border-none gap-1"
            :class="
              isTerminalOpen
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content'
            "
            :title="`Toggle Terminal Panel (${toggleTerminalShortcut})`"
          >
            <Icon icon="codicon:layout-panel" class="h-3.5 w-3.5" />
            <span class="hidden xl:inline">Terminal</span>
          </button>

          <!-- Toggle VCS Right Sidebar Button (VS Code style with same shortcut as Chat View) -->
          <button
            @click="isVCSSidebarOpen = !isVCSSidebarOpen"
            class="hidden md:inline-flex join-item btn btn-xs border-none gap-1"
            :class="
              isVCSSidebarOpen
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content'
            "
            :title="`Toggle VCS Sidebar (${toggleArtifactsShortcut})`"
          >
            <Icon icon="codicon:layout-sidebar-right" class="h-3.5 w-3.5" />
            <span class="hidden xl:inline">Sidebar</span>
          </button>
        </div>
      </div>
    </header>

    <!-- Main VCS Body: Split into Diff Viewer (Middle) & VCSSidebar (Right) -->
    <div class="flex-1 flex overflow-hidden min-h-0 relative">
      <!-- Middle Area: Diff Viewer -->
      <div
        class="flex-1 flex flex-col h-full overflow-hidden min-w-0 relative"
        :class="!isDesktop && mobileActiveTab === 'files' ? 'hidden' : 'flex'"
      >
        <!-- File Subheader (Selected file path & change stats) -->
        <div
          v-if="selectedFile"
          class="px-3 py-1.5 bg-base-200/50 border-b border-base-300 flex items-center justify-between text-xs font-mono shrink-0"
        >
          <div class="flex items-center gap-2 truncate min-w-0">
            <Icon icon="octicon:file-code-24" class="h-3.5 w-3.5 text-primary shrink-0" />
            <span class="font-semibold text-base-content truncate">{{ selectedFile.newPath }}</span>
            <span
              v-if="selectedFile.oldPath && selectedFile.oldPath !== selectedFile.newPath"
              class="text-base-content/50 text-[10px]"
            >
              (renamed from {{ selectedFile.oldPath }})
            </span>
          </div>

          <!-- File Index Indicator -->
          <div class="text-[11px] text-base-content/50 shrink-0">
            {{ selectedIndex + 1 }} / {{ files.length }}
          </div>
        </div>

        <!-- Diff Content Area -->
        <div class="flex-1 overflow-auto min-w-0 relative">
          <!-- Loading -->
          <div
            v-if="loading"
            class="flex items-center justify-center h-full text-base-content/50 gap-3"
          >
            <span class="loading loading-ring loading-md text-primary"></span>
            <span class="text-sm">Loading diff...</span>
          </div>

          <!-- Error -->
          <div v-else-if="errorMsg" class="flex items-center justify-center h-full p-8">
            <div class="alert alert-error max-w-md">
              <Icon icon="mynaui:danger" class="h-5 w-5 shrink-0" />
              <span class="text-sm">{{ errorMsg }}</span>
            </div>
          </div>

          <!-- No changes in current selection -->
          <div
            v-else-if="files.length === 0"
            class="flex flex-col items-center justify-center h-full gap-3 text-base-content/40 p-6 text-center"
          >
            <Icon
              icon="material-symbols:check-circle-outline-rounded"
              class="h-12 w-12 text-success"
            />
            <p class="text-sm font-medium text-base-content/80">
              {{ selectedCommit ? "No changes in this commit" : "Working tree is clean" }}
            </p>
            <p class="text-xs max-w-xs">
              {{
                selectedCommit
                  ? "Select another commit from the right sidebar to inspect changes."
                  : "No unstaged or uncommitted modifications relative to HEAD."
              }}
            </p>
          </div>

          <!-- Diff viewer -->
          <div v-else-if="diffFileObj" class="h-full diff-scroll-container">
            <DiffView
              :diff-file="diffFileObj"
              :diff-view-mode="viewMode"
              :diff-view-theme="theme"
              :diff-view-highlight="true"
              :diff-view-add-widget="true"
              :diff-view-wrap="true"
              @on-add-widget-click="handleAddWidgetClick"
              class="h-full"
            >
              <!-- Comment widget slot — rendered at the active line -->
              <template #widget="{ lineNumber, side, onClose }">
                <div
                  v-if="activeWidget?.side === side && activeWidget?.lineNumber === lineNumber"
                  class="border border-primary/30 bg-base-200 rounded-lg mx-4 my-2 shadow-xl overflow-hidden"
                >
                  <!-- Widget header -->
                  <div
                    class="flex items-center justify-between px-3 py-2 bg-base-300/60 border-b border-base-300"
                  >
                    <div class="flex items-center gap-2 text-xs font-semibold text-base-content/70">
                      <Icon
                        icon="material-symbols:chat-bubble-outline"
                        class="h-4 w-4 text-primary"
                      />
                      <span
                        >Comment · {{ selectedFile?.newPath }} · line {{ lineNumber }} ({{
                          sideName(side)
                        }})</span
                      >
                    </div>
                    <button
                      @click="
                        onClose();
                        closeWidget();
                      "
                      class="btn btn-ghost btn-xs btn-square text-base-content/50 hover:text-base-content"
                    >
                      <Icon icon="mynaui:x" class="h-4 w-4" />
                    </button>
                  </div>

                  <!-- Line preview -->
                  <div class="px-3 py-2 border-b border-base-300/60 bg-base-300/20">
                    <pre
                      class="text-xs font-mono text-base-content/60 whitespace-pre-wrap break-words"
                      >{{ getLineContent(side, lineNumber) }}</pre>
                  </div>

                  <!-- Textarea -->
                  <div class="p-3 space-y-2">
                    <textarea
                      v-model="widgetInput"
                      placeholder="Add a comment… it will appear in the chat input"
                      rows="3"
                      class="textarea textarea-bordered bg-base-100 text-base-content w-full text-xs font-sans resize-none focus:outline-none focus:border-primary"
                      @keydown.ctrl.enter.prevent="submitComment"
                      autofocus
                    ></textarea>
                    <div class="flex items-center justify-between gap-2">
                      <button
                        v-if="hasComment(side, lineNumber)"
                        @click="
                          deleteComment(
                            commentKey(selectedFile?.newPath ?? '', lineNumber, sideName(side)),
                          );
                          onClose();
                        "
                        class="btn btn-ghost btn-xs text-error hover:bg-error/10 gap-1"
                      >
                        <Icon icon="mynaui:trash-one" class="h-3.5 w-3.5" />
                        Delete
                      </button>
                      <div class="flex gap-2 ml-auto">
                        <button
                          @click="
                            onClose();
                            closeWidget();
                          "
                          class="btn btn-ghost btn-xs"
                        >
                          Cancel
                        </button>
                        <button
                          @click="submitComment"
                          :disabled="!widgetInput.trim()"
                          class="btn btn-primary btn-xs gap-1"
                        >
                          <Icon icon="material-symbols:add" class="h-3.5 w-3.5" />
                          Add to Chat
                        </button>
                      </div>
                    </div>
                  </div>
                </div>
              </template>
            </DiffView>
          </div>
        </div>

        <!-- Comment summary bar -->
        <div
          v-if="comments.size > 0"
          class="shrink-0 px-3 py-2 bg-warning/10 border-t border-warning/20 flex items-center gap-2 flex-wrap"
        >
          <Icon icon="material-symbols:chat-bubble-outline" class="h-4 w-4 text-warning shrink-0" />
          <span class="text-xs text-warning font-medium">
            {{ comments.size }} comment{{ comments.size > 1 ? "s" : "" }} added to chat input
          </span>
          <div class="flex items-center gap-1 flex-wrap flex-1 min-w-0">
            <span
              v-for="[key, entry] in Array.from(comments.entries())"
              :key="key"
              class="badge badge-xs badge-warning gap-1 cursor-pointer hover:badge-error transition-colors"
              @click="deleteComment(key)"
              :title="`${entry.filePath}:${entry.lineNumber} — click to remove`"
            >
              {{ entry.filePath.split("/").pop() }}:{{ entry.lineNumber }}
              <Icon icon="mynaui:x" class="h-2.5 w-2.5" />
            </span>
          </div>
          <button
            @click="
              comments.clear();
              emit('update:chatInputText', '');
            "
            class="btn btn-ghost btn-xs text-warning/70 hover:text-error ml-auto shrink-0"
            title="Clear all comments"
          >
            Clear all
          </button>
        </div>
      </div>

      <!-- Right Area: Resizable VCSSidebar -->
      <div
        v-if="isVCSSidebarOpen"
        class="h-full flex relative shrink-0 shadow-xl z-20 md:z-auto"
        :class="[
          !isDesktop
            ? mobileActiveTab === 'files'
              ? 'w-full'
              : 'hidden'
            : {
                'transition-none': isResizingSidebar,
                'transition-[width] duration-200': !isResizingSidebar,
              },
        ]"
        :style="{
          width: isDesktop ? `${vcsSidebarWidth}px` : '100%',
        }"
      >
        <!-- Resizer Handle on Left Edge of VCS Sidebar (Desktop only) -->
        <div
          v-if="isDesktop"
          @mousedown="startSidebarResize"
          class="absolute top-0 left-0 w-1.5 h-full cursor-col-resize hover:bg-primary/50 transition-colors z-30"
          title="Drag to resize VCS sidebar"
        ></div>

        <!-- Sidebar Content Component -->
        <VCSSidebar
          class="w-full h-full"
          :runDir="runDir"
          :files="files"
          :selectedIndex="selectedIndex"
          :commentedFiles="commentedFileList"
          :commits="commits"
          :currentBranch="currentBranch"
          :trackingBranch="trackingBranch"
          :ahead="ahead"
          :behind="behind"
          :unstashedCount="unstashedCount"
          :selectedCommit="selectedCommit"
          :loading="loading"
          @select-file="handleSelectFile"
          @select-commit="handleSelectCommit"
          @refresh="loadGitData"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Let the diff scroll within its container */
.diff-scroll-container {
  overflow: auto;
}

:deep(.d2h-wrapper),
:deep(.diff-view-container) {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
