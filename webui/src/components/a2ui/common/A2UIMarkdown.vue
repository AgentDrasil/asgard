<script setup lang="ts">
import { ref, watch, onMounted } from "vue";
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import type { A2UIMarkdownWidget } from "../../../types/a2ui";
import MarkdownContent from "../../MarkdownContent.vue";
import { fetchWorkspaceAsset } from "../../../utils/a2uiUtils";

const { t } = useI18n();

const props = defineProps<{
  widget: A2UIMarkdownWidget;
  sessionId?: string;
  manifestPath?: string | null;
}>();

const rawContent = ref<string>("");
const isLoading = ref<boolean>(false);
const copied = ref<boolean>(false);

async function loadMarkdownContent() {
  if (props.widget.content) {
    rawContent.value = props.widget.content;
    isLoading.value = false;
    return;
  }

  if (props.widget.sourceMd && props.sessionId) {
    isLoading.value = true;
    try {
      const text = await fetchWorkspaceAsset(
        props.sessionId,
        props.widget.sourceMd,
        props.manifestPath,
      );
      if (text !== null) {
        rawContent.value = text;
      }
    } catch (e) {
      console.error("Failed to load sourceMd for A2UIMarkdown:", e);
    } finally {
      isLoading.value = false;
    }
  }
}

function copyContent() {
  if (!rawContent.value) return;
  navigator.clipboard.writeText(rawContent.value);
  copied.value = true;
  setTimeout(() => {
    copied.value = false;
  }, 2000);
}

onMounted(() => {
  loadMarkdownContent();
});

watch(
  () => [props.widget, props.sessionId, props.manifestPath],
  () => {
    loadMarkdownContent();
  },
  { deep: true },
);
</script>

<template>
  <div
    class="card bg-base-200/80 border border-base-300 p-5 sm:p-7 shadow-xs min-h-[300px] space-y-4 rounded-xl"
  >
    <div class="flex items-center justify-between pb-3 border-b border-base-300">
      <div class="flex items-center gap-2">
        <Icon icon="octicon:file-code-24" class="w-4 h-4 text-primary" />
        <h3 class="text-sm sm:text-base font-bold text-base-content">
          {{ widget.title || widget.sourceMd || t("a2ui.markdown.defaultTitle") }}
        </h3>
      </div>
      <button
        @click="copyContent"
        class="btn btn-xs sm:btn-sm btn-outline border-base-300 gap-1.5 text-xs text-base-content/80 hover:text-base-content"
      >
        <Icon :icon="copied ? 'mynaui:check' : 'mynaui:copy'" class="w-3.5 h-3.5 text-primary" />
        <span>{{ copied ? t("a2ui.markdown.copied") : t("a2ui.markdown.copyMarkdown") }}</span>
      </button>
    </div>

    <div v-if="isLoading" class="flex justify-center items-center py-16">
      <span class="loading loading-spinner text-primary loading-md"></span>
    </div>
    <div v-else class="w-full">
      <MarkdownContent :content="rawContent" />
    </div>
  </div>
</template>
