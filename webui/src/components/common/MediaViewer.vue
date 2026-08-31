<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { Icon } from "@iconify/vue";
import { humanfriendly } from "../../lib/format";

const props = defineProps<{
  src: string;
  fileName: string;
  fileExt: string;
  fileSize?: number;
  mediaCategory: "image" | "video" | "audio" | "pdf" | "binary";
}>();

// Image viewer state
const scale = ref(1);
const rotation = ref(0);
const imageLoading = ref(true);
const imageError = ref(false);
const imgRef = ref<HTMLImageElement | null>(null);
const containerRef = ref<HTMLElement | null>(null);

// Video state
const videoError = ref(false);

// Reset image & video state when src changes
watch(
  () => props.src,
  () => {
    scale.value = 1;
    rotation.value = 0;
    imageLoading.value = true;
    imageError.value = false;
    videoError.value = false;
  },
  { immediate: true },
);

function zoomIn() {
  scale.value = Math.min(scale.value + 0.25, 5);
}

function zoomOut() {
  scale.value = Math.max(scale.value - 0.25, 0.25);
}

function resetZoom() {
  scale.value = 1;
  rotation.value = 0;
}

function fitToWindow() {
  const img = imgRef.value;
  const box = containerRef.value;
  if (!img || !box || !img.naturalWidth || !img.naturalHeight) {
    scale.value = 1;
    return;
  }
  const pad = 32; // container p-4
  const s = Math.min(
    (box.clientWidth - pad) / img.naturalWidth,
    (box.clientHeight - pad) / img.naturalHeight,
    1,
  );
  scale.value = Math.max(0.25, Math.round(s * 100) / 100);
  rotation.value = 0;
}

function rotate() {
  rotation.value = (rotation.value + 90) % 360;
}

const formattedSize = computed(() => {
  if (props.fileSize === undefined || props.fileSize === null) return "";
  return humanfriendly(props.fileSize);
});
</script>

<template>
  <div
    class="media-viewer w-full h-full flex flex-col bg-base-100 relative select-none overflow-hidden"
  >
    <!-- Image View -->
    <template v-if="mediaCategory === 'image'">
      <!-- Image Toolbar -->
      <div
        class="h-10 border-b border-base-300 bg-base-200/60 px-3 flex items-center justify-between z-10"
      >
        <div class="flex items-center gap-2 text-xs text-base-content/70">
          <Icon icon="vscode-icons:file-type-image" class="w-4 h-4" />
          <span class="font-medium text-base-content">{{ fileName }}</span>
          <span v-if="formattedSize" class="text-base-content/50">({{ formattedSize }})</span>
        </div>
        <div class="flex items-center gap-1">
          <button
            class="btn btn-ghost btn-xs btn-square"
            title="Zoom In"
            aria-label="Zoom In"
            @click="zoomIn"
          >
            <Icon icon="octicon:zoom-in-16" class="w-3.5 h-3.5" />
          </button>
          <button
            class="btn btn-ghost btn-xs btn-square"
            title="Zoom Out"
            aria-label="Zoom Out"
            @click="zoomOut"
          >
            <Icon icon="octicon:zoom-out-16" class="w-3.5 h-3.5" />
          </button>
          <button
            class="btn btn-ghost btn-xs btn-square"
            title="Fit to Window"
            aria-label="Fit to Window"
            @click="fitToWindow"
          >
            <Icon icon="octicon:screen-full-16" class="w-3.5 h-3.5" />
          </button>
          <button
            class="btn btn-ghost btn-xs px-2 text-xs"
            title="Reset Zoom"
            aria-label="Reset Zoom"
            @click="resetZoom"
          >
            {{ Math.round(scale * 100) }}%
          </button>
          <button
            class="btn btn-ghost btn-xs btn-square"
            title="Rotate"
            aria-label="Rotate"
            @click="rotate"
          >
            <Icon icon="octicon:sync-16" class="w-3.5 h-3.5" />
          </button>
          <a
            :href="src"
            target="_blank"
            :download="fileName"
            class="btn btn-ghost btn-xs btn-square"
            title="Download / Open Raw"
            aria-label="Download / Open Raw"
          >
            <Icon icon="octicon:download-16" class="w-3.5 h-3.5" />
          </a>
        </div>
      </div>

      <!-- Image Canvas -->
      <div
        ref="containerRef"
        class="flex-1 overflow-auto flex items-center justify-center p-4 relative checkerboard-bg"
      >
        <div
          v-if="imageLoading && !imageError"
          class="absolute inset-0 flex items-center justify-center bg-base-100/60 z-10"
        >
          <span class="loading loading-spinner loading-md text-primary"></span>
        </div>
        <div
          v-if="imageError"
          class="flex flex-col items-center justify-center text-center p-6 text-error gap-2"
        >
          <Icon icon="octicon:alert-16" class="w-8 h-8" />
          <p class="text-sm font-medium">Failed to load image</p>
          <a :href="src" target="_blank" class="btn btn-sm btn-outline mt-2"> Open Directly </a>
        </div>
        <img
          ref="imgRef"
          v-show="!imageError"
          :src="src"
          :alt="fileName"
          class="max-w-none transition-transform duration-100 ease-out shadow-sm rounded border border-base-300/40"
          :style="{
            transform: `scale(${scale}) rotate(${rotation}deg)`,
          }"
          @load="imageLoading = false"
          @error="
            imageLoading = false;
            imageError = true;
          "
        />
      </div>
    </template>

    <!-- Video View -->
    <template v-else-if="mediaCategory === 'video'">
      <div
        class="h-10 border-b border-base-300 bg-base-200/60 px-3 flex items-center justify-between z-10"
      >
        <div class="flex items-center gap-2 text-xs text-base-content/70">
          <Icon icon="vscode-icons:file-type-video" class="w-4 h-4" />
          <span class="font-medium text-base-content">{{ fileName }}</span>
          <span v-if="formattedSize" class="text-base-content/50">({{ formattedSize }})</span>
        </div>
        <div class="flex items-center gap-1">
          <a
            :href="src"
            target="_blank"
            :download="fileName"
            class="btn btn-ghost btn-xs btn-square"
            title="Download / Open Raw"
            aria-label="Download / Open Raw"
          >
            <Icon icon="octicon:download-16" class="w-3.5 h-3.5" />
          </a>
        </div>
      </div>
      <div class="flex-1 overflow-auto flex items-center justify-center p-6 bg-base-300/30">
        <div
          v-if="videoError"
          class="flex flex-col items-center justify-center text-center p-6 text-error gap-2"
        >
          <Icon icon="octicon:alert-16" class="w-8 h-8" />
          <p class="text-sm font-medium">Failed to load video</p>
          <a :href="src" target="_blank" class="btn btn-sm btn-outline mt-2"> Open Directly </a>
        </div>
        <video
          v-show="!videoError"
          :src="src"
          controls
          preload="metadata"
          class="max-w-full max-h-full rounded-lg shadow-md bg-black"
          @error="videoError = true"
        >
          Your browser does not support the video tag.
        </video>
      </div>
    </template>

    <!-- Audio View -->
    <template v-else-if="mediaCategory === 'audio'">
      <div class="flex-1 flex items-center justify-center p-6 bg-base-200/30">
        <div
          class="card bg-base-100 border border-base-300 shadow-md p-6 max-w-md w-full flex flex-col items-center text-center gap-4"
        >
          <div
            class="w-16 h-16 rounded-full bg-primary/10 flex items-center justify-center text-primary"
          >
            <Icon icon="vscode-icons:file-type-audio" class="w-8 h-8" />
          </div>
          <div>
            <h3 class="font-semibold text-base text-base-content">{{ fileName }}</h3>
            <p v-if="formattedSize" class="text-xs text-base-content/50 mt-0.5">
              {{ formattedSize }}
            </p>
          </div>
          <audio :src="src" controls class="w-full mt-2">
            Your browser does not support the audio element.
          </audio>
          <a
            :href="src"
            target="_blank"
            :download="fileName"
            class="btn btn-sm btn-outline gap-1.5 w-full mt-2"
          >
            <Icon icon="octicon:download-16" class="w-4 h-4" />
            Download Audio
          </a>
        </div>
      </div>
    </template>

    <!-- PDF View -->
    <template v-else-if="mediaCategory === 'pdf'">
      <div
        class="h-10 border-b border-base-300 bg-base-200/60 px-3 flex items-center justify-between z-10"
      >
        <div class="flex items-center gap-2 text-xs text-base-content/70">
          <Icon icon="vscode-icons:file-type-pdf" class="w-4 h-4" />
          <span class="font-medium text-base-content">{{ fileName }}</span>
          <span v-if="formattedSize" class="text-base-content/50">({{ formattedSize }})</span>
        </div>
        <div class="flex items-center gap-1">
          <a
            :href="src"
            target="_blank"
            class="btn btn-ghost btn-xs gap-1 text-xs"
            title="Open in New Tab"
          >
            <Icon icon="octicon:link-external-16" class="w-3.5 h-3.5" />
            <span>Open in Tab</span>
          </a>
          <a
            :href="src"
            target="_blank"
            :download="fileName"
            class="btn btn-ghost btn-xs btn-square"
            title="Download"
            aria-label="Download"
          >
            <Icon icon="octicon:download-16" class="w-3.5 h-3.5" />
          </a>
        </div>
      </div>
      <div class="flex-1 w-full h-full bg-base-200">
        <iframe :src="src" class="w-full h-full border-0" title="PDF Viewer"></iframe>
      </div>
    </template>

    <!-- Binary Fallback View -->
    <template v-else>
      <div class="flex-1 flex items-center justify-center p-6 bg-base-200/30">
        <div
          class="card bg-base-100 border border-base-300 shadow-md p-6 max-w-sm w-full flex flex-col items-center text-center gap-4"
        >
          <div
            class="w-16 h-16 rounded-2xl bg-base-200 flex items-center justify-center text-base-content/60"
          >
            <Icon icon="octicon:file-binary-24" class="w-8 h-8" />
          </div>
          <div>
            <h3 class="font-semibold text-base text-base-content break-all">{{ fileName }}</h3>
            <p class="text-xs text-base-content/50 mt-1">
              Binary file <span v-if="formattedSize">({{ formattedSize }})</span>
            </p>
            <p class="text-xs text-base-content/60 mt-2">
              This file cannot be previewed as text. You can download or open it directly.
            </p>
          </div>
          <a
            :href="src"
            target="_blank"
            :download="fileName"
            class="btn btn-primary btn-sm gap-1.5 w-full mt-2"
          >
            <Icon icon="octicon:download-16" class="w-4 h-4" />
            Download File
          </a>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.checkerboard-bg {
  background-color: var(--b2, #f8f9fa);
  background-image:
    linear-gradient(45deg, rgba(0, 0, 0, 0.05) 25%, transparent 25%),
    linear-gradient(-45deg, rgba(0, 0, 0, 0.05) 25%, transparent 25%),
    linear-gradient(45deg, transparent 75%, rgba(0, 0, 0, 0.05) 75%),
    linear-gradient(-45deg, transparent 75%, rgba(0, 0, 0, 0.05) 75%);
  background-size: 16px 16px;
  background-position:
    0 0,
    0 8px,
    8px -8px,
    -8px 0px;
}
</style>
