<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, nextTick } from "vue";
import { Icon } from "@iconify/vue";
import DOMPurify from "dompurify";
import { useMermaid } from "../composables/useMermaid";

const props = withDefaults(
  defineProps<{
    code: string;
    title?: string;
  }>(),
  {
    title: "Mermaid Diagram",
  },
);

const { activeMermaidTheme, renderDiagram } = useMermaid();

const svgHtml = ref<string>("");
const errorMessage = ref<string | null>(null);
const isLoading = ref<boolean>(false);
const zoomLevel = ref<number>(1.0);
const panX = ref<number>(0);
const panY = ref<number>(0);
const isPanning = ref<boolean>(false);
const isFullscreen = ref<boolean>(false);
const showRaw = ref<boolean>(false);
const isCopied = ref<boolean>(false);

const canvasRef = ref<HTMLElement | null>(null);
const fullscreenCanvasRef = ref<HTMLElement | null>(null);

let panStartX = 0;
let panStartY = 0;
let initialPanX = 0;
let initialPanY = 0;
let copyTimeout: ReturnType<typeof setTimeout> | null = null;
let debounceTimer: ReturnType<typeof setTimeout> | null = null;
let renderToken = 0;
let latestBindFunctions: ((element: Element) => void) | undefined;

async function renderChart(): Promise<void> {
  const token = ++renderToken;
  const source = props.code?.trim();

  if (!source) {
    svgHtml.value = "";
    errorMessage.value = null;
    isLoading.value = false;
    latestBindFunctions = undefined;
    return;
  }

  isLoading.value = true;
  errorMessage.value = null;

  try {
    const result = await renderDiagram(source);
    if (token !== renderToken) return;

    if (result.error) {
      errorMessage.value = result.error;
      svgHtml.value = "";
      latestBindFunctions = undefined;
    } else {
      svgHtml.value = DOMPurify.sanitize(result.svg || "", {
        USE_PROFILES: { svg: true, svgFilters: true, html: true },
      });
      errorMessage.value = null;
      latestBindFunctions = result.bindFunctions;

      await nextTick();
      if (token !== renderToken) return;

      if (latestBindFunctions) {
        if (canvasRef.value) {
          latestBindFunctions(canvasRef.value);
        }
        if (isFullscreen.value && fullscreenCanvasRef.value) {
          latestBindFunctions(fullscreenCanvasRef.value);
        }
      }
    }
  } catch (err: unknown) {
    if (token !== renderToken) return;
    errorMessage.value = err instanceof Error ? err.message : String(err);
    svgHtml.value = "";
    latestBindFunctions = undefined;
  } finally {
    if (token === renderToken) {
      isLoading.value = false;
    }
  }
}

function handleZoomIn(): void {
  zoomLevel.value = Math.min(5.0, Number((zoomLevel.value * 1.2).toFixed(2)));
}

function handleZoomOut(): void {
  zoomLevel.value = Math.max(0.2, Number((zoomLevel.value / 1.2).toFixed(2)));
}

function handleResetZoom(): void {
  zoomLevel.value = 1.0;
  panX.value = 0;
  panY.value = 0;
}

function handleWheel(e: WheelEvent): void {
  e.preventDefault();
  const delta = e.deltaY < 0 ? 1.15 : 0.85;
  const next = Math.min(5.0, Math.max(0.2, Number((zoomLevel.value * delta).toFixed(2))));
  zoomLevel.value = next;
}

function handleMouseDown(e: MouseEvent): void {
  if (e.button !== 0) return; // Only primary button
  isPanning.value = true;
  panStartX = e.clientX;
  panStartY = e.clientY;
  initialPanX = panX.value;
  initialPanY = panY.value;

  window.addEventListener("mousemove", handleMouseMove);
  window.addEventListener("mouseup", handleMouseUp);
}

function handleMouseMove(e: MouseEvent): void {
  if (!isPanning.value) return;
  panX.value = initialPanX + (e.clientX - panStartX);
  panY.value = initialPanY + (e.clientY - panStartY);
}

function handleMouseUp(): void {
  isPanning.value = false;
  window.removeEventListener("mousemove", handleMouseMove);
  window.removeEventListener("mouseup", handleMouseUp);
}

function handleToggleFullscreen(): void {
  isFullscreen.value = !isFullscreen.value;
  handleResetZoom();
}

function handleKeyDown(e: KeyboardEvent): void {
  if (e.key === "Escape" && isFullscreen.value) {
    isFullscreen.value = false;
    handleResetZoom();
  }
}

async function handleCopy(): Promise<void> {
  if (!props.code) return;
  try {
    await navigator.clipboard.writeText(props.code);
    isCopied.value = true;
    if (copyTimeout) clearTimeout(copyTimeout);
    copyTimeout = setTimeout(() => {
      isCopied.value = false;
    }, 2000);
  } catch {
    // Clipboard write failed
  }
}

// Debounce code watcher to avoid full re-rendering on every streaming token chunk
watch(
  () => props.code,
  () => {
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      renderChart();
    }, 300);
  },
);

watch(activeMermaidTheme, () => {
  renderChart();
});

watch(isFullscreen, async (val) => {
  if (val) {
    window.addEventListener("keydown", handleKeyDown);
    await nextTick();
    if (latestBindFunctions && fullscreenCanvasRef.value) {
      latestBindFunctions(fullscreenCanvasRef.value);
    }
  } else {
    window.removeEventListener("keydown", handleKeyDown);
  }
});

onMounted(() => {
  renderChart();
});

onUnmounted(() => {
  window.removeEventListener("mousemove", handleMouseMove);
  window.removeEventListener("mouseup", handleMouseUp);
  window.removeEventListener("keydown", handleKeyDown);
  if (copyTimeout) clearTimeout(copyTimeout);
  if (debounceTimer) clearTimeout(debounceTimer);
});
</script>

<template>
  <div
    class="my-4 flex flex-col rounded-lg border border-base-300 bg-base-100/50 shadow-xs overflow-hidden"
  >
    <!-- Header Toolbar -->
    <div
      class="flex items-center justify-between border-b border-base-300 bg-base-200/60 px-3 py-1.5 text-xs select-none"
    >
      <div class="flex items-center gap-1.5 font-medium text-base-content/80">
        <Icon icon="material-symbols:account-tree-outline" class="h-4 w-4 text-primary" />
        <span>{{ title }}</span>
        <span
          v-if="zoomLevel !== 1.0 && !showRaw && !errorMessage"
          class="rounded bg-base-300/80 px-1.5 py-0.5 text-[10px] font-mono text-base-content/70"
        >
          {{ Math.round(zoomLevel * 100) }}%
        </span>
      </div>

      <div class="flex items-center gap-1">
        <!-- Toggle Raw Code / Diagram -->
        <button
          class="btn btn-ghost btn-xs h-7 px-2 text-base-content/70 hover:text-base-content"
          :class="{ 'btn-active bg-base-300/60 text-primary': showRaw }"
          :title="showRaw ? 'Show Diagram' : 'Show Mermaid Code'"
          type="button"
          @click="showRaw = !showRaw"
        >
          <Icon
            :icon="showRaw ? 'material-symbols:preview' : 'material-symbols:code'"
            class="h-3.5 w-3.5"
          />
          <span class="ml-1 hidden sm:inline">{{ showRaw ? "Chart" : "Code" }}</span>
        </button>

        <!-- Copy Code Button -->
        <button
          class="btn btn-ghost btn-xs h-7 px-2 text-base-content/70 hover:text-base-content"
          :title="isCopied ? 'Copied!' : 'Copy Mermaid Code'"
          type="button"
          @click="handleCopy"
        >
          <Icon
            :icon="
              isCopied ? 'material-symbols:check-rounded' : 'material-symbols:content-copy-outline'
            "
            class="h-3.5 w-3.5"
            :class="{ 'text-success': isCopied }"
          />
        </button>

        <template v-if="!showRaw && !errorMessage">
          <div class="h-3 w-px bg-base-300 mx-0.5" />

          <!-- Zoom In -->
          <button
            class="btn btn-ghost btn-xs h-7 px-1.5 text-base-content/70 hover:text-base-content"
            title="Zoom In"
            type="button"
            @click="handleZoomIn"
          >
            <Icon icon="fluent:zoom-in-24-regular" class="h-3.5 w-3.5" />
          </button>

          <!-- Zoom Out -->
          <button
            class="btn btn-ghost btn-xs h-7 px-1.5 text-base-content/70 hover:text-base-content"
            title="Zoom Out"
            type="button"
            @click="handleZoomOut"
          >
            <Icon icon="fluent:zoom-out-24-regular" class="h-3.5 w-3.5" />
          </button>

          <!-- Reset Zoom -->
          <button
            class="btn btn-ghost btn-xs h-7 px-1.5 text-base-content/70 hover:text-base-content"
            title="Reset View"
            type="button"
            @click="handleResetZoom"
          >
            <Icon icon="fluent:arrow-reset-24-regular" class="h-3.5 w-3.5" />
          </button>

          <!-- Fullscreen -->
          <button
            class="btn btn-ghost btn-xs h-7 px-1.5 text-base-content/70 hover:text-base-content"
            title="Fullscreen Preview"
            type="button"
            @click="handleToggleFullscreen"
          >
            <Icon icon="fluent:full-screen-maximize-24-regular" class="h-3.5 w-3.5" />
          </button>
        </template>
      </div>
    </div>

    <!-- Error Alert Fallback -->
    <div v-if="errorMessage" class="p-3 bg-base-200/40">
      <div class="alert alert-warning text-xs mb-2 py-2 px-3 flex items-start gap-2 shadow-xs">
        <Icon icon="mynaui:danger" class="h-4 w-4 shrink-0 mt-0.5 text-warning" />
        <div class="min-w-0 flex-1">
          <div class="font-semibold">Mermaid Syntax Error</div>
          <div class="text-[11px] opacity-90 break-words font-mono mt-0.5">{{ errorMessage }}</div>
        </div>
      </div>
      <pre
        class="overflow-x-auto rounded-md bg-base-300/50 p-3 font-mono text-xs text-base-content/80 leading-relaxed border border-base-300"
      ><code>{{ code }}</code></pre>
    </div>

    <!-- Raw Source View -->
    <div v-else-if="showRaw" class="p-3 bg-base-200/30">
      <pre
        class="overflow-x-auto rounded-md bg-base-300/50 p-3 font-mono text-xs text-base-content/90 leading-relaxed border border-base-300"
      ><code>{{ code }}</code></pre>
    </div>

    <!-- Diagram Canvas View -->
    <div
      v-else
      ref="canvasRef"
      class="relative flex min-h-[180px] max-h-[500px] w-full items-center justify-center overflow-hidden bg-base-100/30 select-none"
      :class="[isPanning ? 'cursor-grabbing' : 'cursor-grab']"
      @mousedown="handleMouseDown"
      @wheel="handleWheel"
    >
      <!-- Loading indicator -->
      <div
        v-if="isLoading"
        class="absolute inset-0 z-10 flex items-center justify-center bg-base-100/60 backdrop-blur-xs"
      >
        <span class="loading loading-spinner loading-md text-primary" />
      </div>

      <!-- SVG Content Wrapper (Inline view keeps rendered DOM mounted for reference stability) -->
      <div
        class="flex items-center justify-center p-4 transition-transform duration-75 ease-out will-change-transform"
        :style="{
          transform: `translate(${panX}px, ${panY}px) scale(${zoomLevel})`,
          transformOrigin: 'center center',
        }"
        v-html="svgHtml"
      />
    </div>
  </div>

  <!-- Fullscreen Modal Overlay -->
  <Teleport to="body">
    <div
      v-if="isFullscreen"
      class="fixed inset-0 z-50 flex flex-col bg-base-100/95 backdrop-blur-md animate-fade-in"
    >
      <!-- Fullscreen Header -->
      <div
        class="flex items-center justify-between border-b border-base-300 bg-base-200/80 px-4 py-2.5 shadow-sm"
      >
        <div class="flex items-center gap-2 font-medium text-sm text-base-content">
          <Icon icon="material-symbols:account-tree-outline" class="h-5 w-5 text-primary" />
          <span>{{ title }}</span>
          <span
            v-if="zoomLevel !== 1.0"
            class="rounded bg-base-300 px-2 py-0.5 text-xs font-mono text-base-content/70"
          >
            {{ Math.round(zoomLevel * 100) }}%
          </span>
        </div>

        <div class="flex items-center gap-1.5">
          <!-- Zoom Controls in Fullscreen -->
          <button
            class="btn btn-ghost btn-sm h-8 px-2 text-base-content/70 hover:text-base-content"
            title="Zoom In"
            type="button"
            @click="handleZoomIn"
          >
            <Icon icon="fluent:zoom-in-24-regular" class="h-4 w-4" />
          </button>
          <button
            class="btn btn-ghost btn-sm h-8 px-2 text-base-content/70 hover:text-base-content"
            title="Zoom Out"
            type="button"
            @click="handleZoomOut"
          >
            <Icon icon="fluent:zoom-out-24-regular" class="h-4 w-4" />
          </button>
          <button
            class="btn btn-ghost btn-sm h-8 px-2 text-base-content/70 hover:text-base-content"
            title="Reset View"
            type="button"
            @click="handleResetZoom"
          >
            <Icon icon="fluent:arrow-reset-24-regular" class="h-4 w-4" />
          </button>

          <div class="h-4 w-px bg-base-300 mx-1" />

          <!-- Copy Button -->
          <button
            class="btn btn-ghost btn-sm h-8 px-2 text-base-content/70 hover:text-base-content"
            :title="isCopied ? 'Copied!' : 'Copy Mermaid Code'"
            type="button"
            @click="handleCopy"
          >
            <Icon
              :icon="
                isCopied
                  ? 'material-symbols:check-rounded'
                  : 'material-symbols:content-copy-outline'
              "
              class="h-4 w-4"
              :class="{ 'text-success': isCopied }"
            />
          </button>

          <!-- Close Fullscreen -->
          <button
            class="btn btn-ghost btn-sm btn-circle h-8 w-8 text-base-content/70 hover:text-base-content hover:bg-base-300"
            title="Exit Fullscreen (Esc)"
            type="button"
            @click="handleToggleFullscreen"
          >
            <Icon icon="material-symbols:close" class="h-5 w-5" />
          </button>
        </div>
      </div>

      <!-- Fullscreen Canvas -->
      <div
        ref="fullscreenCanvasRef"
        class="relative flex flex-1 w-full items-center justify-center overflow-hidden select-none"
        :class="[isPanning ? 'cursor-grabbing' : 'cursor-grab']"
        @mousedown="handleMouseDown"
        @wheel="handleWheel"
      >
        <!-- Loading indicator in fullscreen -->
        <div
          v-if="isLoading"
          class="absolute inset-0 z-10 flex items-center justify-center bg-base-100/60 backdrop-blur-xs"
        >
          <span class="loading loading-spinner loading-lg text-primary" />
        </div>

        <!-- Error fallback in fullscreen -->
        <div v-if="errorMessage" class="max-w-2xl p-4">
          <div class="alert alert-warning text-sm mb-3 py-2 px-3 flex items-start gap-2 shadow-xs">
            <Icon icon="mynaui:danger" class="h-5 w-5 shrink-0 mt-0.5 text-warning" />
            <div class="min-w-0 flex-1">
              <div class="font-semibold">Mermaid Syntax Error</div>
              <div class="text-xs opacity-90 break-words font-mono mt-1">{{ errorMessage }}</div>
            </div>
          </div>
          <pre
            class="overflow-x-auto rounded-md bg-base-300/50 p-4 font-mono text-xs text-base-content/80 leading-relaxed border border-base-300"
          ><code>{{ code }}</code></pre>
        </div>

        <!-- Diagram in fullscreen -->
        <div
          v-else
          class="flex items-center justify-center p-8 transition-transform duration-75 ease-out will-change-transform"
          :style="{
            transform: `translate(${panX}px, ${panY}px) scale(${zoomLevel})`,
            transformOrigin: 'center center',
          }"
          v-html="svgHtml"
        />
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
:deep(svg) {
  max-width: 100%;
  height: auto;
  user-select: none;
}
</style>
