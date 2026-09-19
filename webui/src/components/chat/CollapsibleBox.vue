<script setup lang="ts">
import { ref, useId } from "vue";
import { Icon } from "@iconify/vue";

withDefaults(
  defineProps<{
    open?: boolean;
    containerClass?: string;
    titleClass?: string;
    contentClass?: string;
  }>(),
  {
    open: false,
    containerClass: "bg-base-200/40 border border-base-300",
    titleClass: "font-mono font-medium text-base-content/70",
    contentClass: "border-t border-base-300/40 pt-3",
  },
);

const details = ref<HTMLDetailsElement | null>(null);
const contentId = useId();

function collapse() {
  if (details.value) details.value.open = false;
}
</script>

<template>
  <details
    ref="details"
    :open="open"
    :class="['collapse collapse-arrow rounded-lg text-xs w-full min-w-0', containerClass]"
  >
    <summary
      :class="[
        'collapse-title cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none',
        titleClass,
      ]"
    >
      <slot name="title" />
    </summary>
    <div :id="contentId" :class="['collapse-content min-w-0', contentClass]">
      <slot />
      <div class="flex justify-end pt-1">
        <button
          type="button"
          :aria-controls="contentId"
          aria-expanded="true"
          :title="$t('chat.collapse')"
          class="btn btn-xs btn-ghost gap-1 text-base-content/60 normal-case"
          @click.stop.prevent="collapse"
        >
          <Icon icon="material-symbols:keyboard-arrow-up-rounded" class="h-4 w-4" />
          {{ $t("chat.collapse") }}
        </button>
      </div>
    </div>
  </details>
</template>
