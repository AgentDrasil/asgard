<script setup lang="ts">
import { ref, watch, computed, nextTick } from "vue";
import { Icon } from "@iconify/vue";
import DOMPurify from "dompurify";
import { useI18n } from "vue-i18n";
import { useShiki } from "../composables/useShiki";
import { useInPageFind } from "../composables/useInPageFind";
import { getFileIcon, resolveViewerCategory, isCsvFile } from "../utils/fileUtils";
import { getRawWorkspaceFileUrl } from "../lib/api";
import MarkdownContent from "./MarkdownContent.vue";
import FindBar from "./FindBar.vue";
import MediaViewer from "./common/MediaViewer.vue";
import CsvViewer from "./common/CsvViewer.vue";
import A2UIRenderer from "./a2ui/A2UIRenderer.vue";
import { isA2UIManifest, parseA2UIManifest } from "../utils/a2uiUtils";

const { t } = useI18n();
const { highlightBlock } = useShiki();

const props = withDefaults(
  defineProps<{
    sessionId: string;
    activeFilePath: string | null;
    modifiedFiles: string[];
    isExpanded?: boolean;
  }>(),
  {
    isExpanded: false,
  },
);

const emit = defineEmits<{
  (e: "close"): void;
  (e: "select-file", path: string): void;
  (e: "toggle-expand"): void;
}>();

interface FileData {
  path: string;
  name: string;
  ext: string;
  size: number;
  content: string;
  isBinary?: boolean;
  updatedAt: string;
}

const fileData = ref<FileData | null>(null);
const loading = ref(false);
const errorMsg = ref<string | null>(null);
const markdownViewMode = ref<"rendered" | "source">("rendered");
const csvViewMode = ref<"table" | "source">("table");
const a2uiViewMode = ref<"dashboard" | "source">("dashboard");

const isA2UI = computed(() => {
  if (!fileData.value) return false;
  return isA2UIManifest(fileData.value.name, fileData.value.path, fileData.value.content);
});

const parsedA2UIManifest = computed(() => {
  if (!isA2UI.value || !fileData.value?.content) return null;
  return parseA2UIManifest(fileData.value.content);
});

const isMarkdown = computed(() => {
  if (!fileData.value) return false;
  const ext = fileData.value.ext.toLowerCase();
  return ext === "md" || ext === "markdown";
});

const isCsv = computed(() => {
  if (!fileData.value) return false;
  return isCsvFile(fileData.value.ext, fileData.value.path);
});

const viewerCategory = computed<
  "image" | "video" | "audio" | "pdf" | "csv" | "binary" | "markdown" | "code"
>(() => {
  return resolveViewerCategory(fileData.value);
});

const isMedia = computed(() => {
  const cat = viewerCategory.value;
  return cat === "image" || cat === "video" || cat === "audio" || cat === "pdf";
});

const isMediaPreview = computed(() => {
  const cat = viewerCategory.value;
  return cat === "image" || cat === "video" || cat === "audio" || cat === "pdf" || cat === "binary";
});

const rawUrl = computed(() => {
  if (!props.sessionId || !fileData.value?.path) return "";
  return getRawWorkspaceFileUrl(props.sessionId, fileData.value.path);
});

const highlightedArtifactContent = computed(() => {
  if (!fileData.value || !fileData.value.content) return "";
  const ext = fileData.value.ext.toLowerCase();
  const lang = isMarkdown.value ? "markdown" : isCsv.value ? "csv" : ext || "text";
  const highlighted = highlightBlock(fileData.value.content, lang, [
    "rounded-lg",
    "p-4",
    "whitespace-pre-wrap",
    "break-words",
    "border",
    "border-base-300",
    "text-xs",
    "font-mono",
    "leading-relaxed",
    "min-h-[120px]",
    "w-full",
  ]);
  if (highlighted) return highlighted;
  // Fallback while Shiki loads or when it fails: keep the same wrapping so the
  // layout doesn't jump once highlighting kicks in.
  return `<pre class="font-mono text-xs text-base-content bg-base-200/80 p-4 rounded-lg border border-base-300 leading-relaxed whitespace-pre-wrap break-words min-h-[120px] w-full"><code>${DOMPurify.sanitize(fileData.value.content)}</code></pre>`;
});

function formatPath(path: string): string {
  if (!path) return "";
  return path.replace(/^\/home\/[^/]+/, "~");
}

async function fetchFile(path: string) {
  if (!path || !props.sessionId) return;
  loading.value = true;
  errorMsg.value = null;

  try {
    const res = await fetch(
      `/api/v1/workspace/file?session_id=${encodeURIComponent(props.sessionId)}&path=${encodeURIComponent(path)}`,
    );
    if (!res.ok) {
      const errJson = await res.json().catch(() => ({}));
      throw new Error(errJson.error || `HTTP ${res.status}`);
    }
    const data: FileData = await res.json();
    fileData.value = data;
    if (isCsvFile(data.ext, data.path)) {
      csvViewMode.value = "table";
    }
    if (isA2UIManifest(data.name, data.path, data.content)) {
      a2uiViewMode.value = "dashboard";
    }
  } catch (err: any) {
    errorMsg.value = err.message || "Failed to load file";
    fileData.value = null;
  } finally {
    loading.value = false;
  }
}

watch(
  [() => props.activeFilePath, () => props.sessionId],
  ([newPath]) => {
    if (newPath) {
      fetchFile(newPath);
    } else {
      fileData.value = null;
    }
  },
  { immediate: true },
);

function onFileSelectChange(event: Event) {
  const target = event.target as HTMLSelectElement;
  if (target && target.value) {
    emit("select-file", target.value);
  }
}

const contentContainerRef = ref<HTMLElement | null>(null);
const findState = useInPageFind(contentContainerRef);

// When file content or view mode changes, re-run active search if find bar is open
watch([() => fileData.value, markdownViewMode, csvViewMode, a2uiViewMode], () => {
  if (
    findState.isOpen.value &&
    findState.query.value.trim() &&
    !isMediaPreview.value &&
    (!isCsv.value || csvViewMode.value === "source") &&
    (!isA2UI.value || a2uiViewMode.value === "source")
  ) {
    nextTick(() => {
      findState.performSearch();
    });
  } else {
    findState.clearHighlights();
  }
});
</script>

<template>
  <div
    class="w-full h-full flex flex-col bg-base-100 border-l border-base-300 text-base-content select-text overflow-hidden shadow-2xl relative"
  >
    <!-- Header & Dropdown File Selection Bar -->
    <div
      class="flex items-center justify-between px-3 py-2.5 sm:px-6 sm:py-3.5 bg-base-200 border-b border-base-300 shrink-0 shadow-sm min-h-[52px]"
    >
      <div class="flex items-center gap-2 overflow-hidden flex-1 min-w-0 mr-2">
        <!-- Mobile Back Button -->
        <button
          @click="emit('close')"
          class="md:hidden btn btn-sm btn-ghost btn-square text-base-content/80 hover:text-base-content shrink-0"
          :title="t('viewers.backToChat')"
        >
          <Icon icon="material-symbols:arrow-back-ios-rounded" class="h-4 w-4 ml-1" />
        </button>

        <span
          class="hidden sm:flex text-[11px] font-bold uppercase tracking-wider text-emerald-500 bg-emerald-500/10 border border-emerald-500/30 px-2 h-8 rounded items-center gap-1.5 shrink-0"
        >
          {{ t("viewers.artifacts") }}
        </span>

        <!-- Dropdown Selector for Files -->
        <div class="relative flex-1 min-w-0">
          <div class="relative flex items-center w-full">
            <Icon
              :icon="getFileIcon(undefined, activeFilePath || '')"
              class="absolute left-2.5 h-4 w-4 shrink-0 text-emerald-500 pointer-events-none z-10"
            />
            <select
              :value="activeFilePath || ''"
              @change="onFileSelectChange"
              class="select select-sm select-bordered w-full pl-8 pr-8 font-mono text-xs text-base-content bg-base-100 focus:outline-none focus:border-emerald-500 truncate"
              :title="t('viewers.selectFileToPreview')"
            >
              <option v-for="file in modifiedFiles" :key="file" :value="file">
                {{ formatPath(file) }}
              </option>
            </select>
          </div>
        </div>
      </div>

      <div class="flex items-center gap-1.5 shrink-0">
        <!-- Markdown Toggle -->
        <div v-if="isMarkdown" class="join bg-base-300/60 p-0.5 rounded-lg">
          <button
            @click="markdownViewMode = 'rendered'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              markdownViewMode === 'rendered'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            :title="t('viewers.renderedPreviewTitle')"
          >
            <Icon icon="material-symbols:preview" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t("viewers.preview") }}</span>
          </button>
          <button
            @click="markdownViewMode = 'source'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              markdownViewMode === 'source'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            :title="t('viewers.rawMarkdownSourceTitle')"
          >
            <Icon icon="material-symbols:code" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t("viewers.source") }}</span>
          </button>
        </div>

        <!-- CSV Toggle -->
        <div v-if="isCsv" class="join bg-base-300/60 p-0.5 rounded-lg">
          <button
            @click="csvViewMode = 'table'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              csvViewMode === 'table'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            :title="t('viewers.tablePreviewTitle')"
          >
            <Icon icon="octicon:table-24" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t("viewers.table") }}</span>
          </button>
          <button
            @click="csvViewMode = 'source'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              csvViewMode === 'source'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            :title="t('viewers.rawCsvSourceTitle')"
          >
            <Icon icon="material-symbols:code" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t("viewers.source") }}</span>
          </button>
        </div>

        <!-- A2UI Dashboard Toggle -->
        <div v-if="isA2UI && parsedA2UIManifest" class="join bg-base-300/60 p-0.5 rounded-lg">
          <button
            @click="a2uiViewMode = 'dashboard'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              a2uiViewMode === 'dashboard'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            :title="t('viewers.dashboardPreviewTitle')"
          >
            <Icon icon="material-symbols:dashboard-customize-outline" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t("viewers.dashboard") }}</span>
          </button>
          <button
            @click="a2uiViewMode = 'source'"
            :class="[
              'join-item btn btn-xs border-none font-medium gap-1',
              a2uiViewMode === 'source'
                ? 'btn-primary shadow-xs'
                : 'btn-ghost text-base-content/70 hover:text-base-content',
            ]"
            :title="t('viewers.rawJsonSourceTitle')"
          >
            <Icon icon="material-symbols:code" class="h-3.5 w-3.5" />
            <span class="hidden sm:inline">{{ t("viewers.source") }}</span>
          </button>
        </div>

        <!-- Open Raw / Download Button for Media -->
        <a
          v-if="isMedia && rawUrl"
          :href="rawUrl"
          target="_blank"
          rel="noopener noreferrer"
          :download="fileData?.name"
          class="p-1.5 text-xs rounded bg-base-300 hover:bg-base-300/80 text-base-content transition-colors border border-base-300 inline-flex items-center"
          :title="t('viewers.openInNewWindow')"
        >
          <Icon icon="octicon:link-external-16" class="h-3.5 w-3.5" />
        </a>

        <!-- Find Button (only for text/code/markdown/raw csv/raw json) -->
        <button
          v-if="
            !isMediaPreview &&
            (!isCsv || csvViewMode === 'source') &&
            (!isA2UI || a2uiViewMode === 'source')
          "
          @click="findState.toggle()"
          :class="[
            'p-1.5 text-xs rounded transition-colors border',
            findState.isOpen.value
              ? 'bg-primary text-primary-content border-primary shadow-xs'
              : 'bg-base-300 hover:bg-base-300/80 text-base-content border-base-300',
          ]"
          :title="t('viewers.findInArtifact')"
        >
          <Icon icon="material-symbols:search" class="h-3.5 w-3.5" />
        </button>

        <button
          @click="fetchFile(activeFilePath || '')"
          class="p-1.5 text-xs rounded bg-base-300 hover:bg-base-300/80 text-base-content transition-colors border border-base-300"
          :title="t('viewers.refreshFileContent')"
        >
          <Icon icon="octicon:sync-24" class="h-3.5 w-3.5" />
        </button>

        <!-- Fullscreen / Expand Context Area Toggle Button -->
        <button
          @click="emit('toggle-expand')"
          class="p-1.5 text-xs rounded transition-colors border border-base-300"
          :class="
            isExpanded
              ? 'bg-primary text-primary-content border-primary shadow-xs'
              : 'bg-base-300 hover:bg-base-300/80 text-base-content'
          "
          :title="isExpanded ? t('viewers.exitFullScreen') : t('viewers.fullScreen')"
        >
          <Icon
            :icon="isExpanded ? 'octicon:screen-normal-24' : 'octicon:screen-full-24'"
            class="h-3.5 w-3.5"
          />
        </button>
      </div>
    </div>

    <!-- Floating In-Page Find Bar -->
    <FindBar
      v-if="
        !isMediaPreview &&
        (!isCsv || csvViewMode === 'source') &&
        (!isA2UI || a2uiViewMode === 'source')
      "
      v-model="findState.query.value"
      :isOpen="findState.isOpen.value"
      :currentIndex="findState.currentIndex.value"
      :totalMatches="findState.totalMatches.value"
      @next="findState.findNext"
      @prev="findState.findPrev"
      @close="findState.close"
    />

    <!-- Content Preview Area -->
    <div
      ref="contentContainerRef"
      class="flex-1 overflow-y-auto relative bg-base-100"
      :class="isMediaPreview || (isCsv && csvViewMode === 'table') ? 'p-0' : 'p-3 sm:p-5'"
    >
      <div
        v-if="loading"
        class="flex items-center justify-center h-full text-base-content/60 text-sm gap-2"
      >
        <div
          class="w-4 h-4 border-2 border-emerald-500 border-t-transparent rounded-full animate-spin"
        ></div>
        <span>{{ t("viewers.loadingContent") }}</span>
      </div>

      <div
        v-else-if="errorMsg"
        class="flex flex-col items-center justify-center h-full text-error text-sm gap-2 p-6 text-center"
      >
        <span>⚠️ {{ errorMsg }}</span>
        <button
          @click="fetchFile(activeFilePath || '')"
          class="mt-2 px-3 py-1 bg-base-300 text-base-content text-xs rounded hover:bg-base-300/80"
        >
          {{ t("viewers.retry") }}
        </button>
      </div>

      <div v-else-if="fileData" class="h-full">
        <div
          v-if="
            viewerCategory === 'image' ||
            viewerCategory === 'video' ||
            viewerCategory === 'audio' ||
            viewerCategory === 'pdf' ||
            viewerCategory === 'binary'
          "
          class="w-full h-full min-w-0"
        >
          <MediaViewer
            :src="viewerCategory === 'binary' ? '' : rawUrl"
            :fileName="fileData.name"
            :fileExt="fileData.ext"
            :fileSize="fileData.size"
            :mediaCategory="viewerCategory"
          />
        </div>
        <div
          v-else-if="isA2UI && parsedA2UIManifest && a2uiViewMode === 'dashboard'"
          class="w-full min-w-0"
        >
          <A2UIRenderer
            :manifest="parsedA2UIManifest"
            :sessionId="sessionId"
            :manifestPath="fileData.path"
          />
        </div>
        <div v-else-if="isCsv && csvViewMode === 'table'" class="w-full h-full min-w-0">
          <CsvViewer :content="fileData.content" :fileName="fileData.name" />
        </div>
        <div
          v-else-if="isMarkdown && markdownViewMode === 'rendered'"
          class="w-full h-full min-w-0"
        >
          <MarkdownContent :content="fileData.content" />
        </div>
        <div v-else v-html="highlightedArtifactContent" class="w-full h-full min-w-0"></div>
      </div>

      <div v-else class="flex items-center justify-center h-full text-base-content/50 text-sm">
        {{ t("viewers.selectToPreview") }}
      </div>
    </div>
  </div>
</template>
