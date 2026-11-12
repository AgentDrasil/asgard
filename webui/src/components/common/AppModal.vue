<script setup lang="ts">
import { ref, watch, onMounted } from "vue";

// Shared modal built on the native <dialog>: showModal() provides the top
// layer, inert page, focus trap/restore and Esc handling. The DaisyUI `.modal`
// element spans the viewport, so a press-and-release on the dialog element
// itself (not its content) is a backdrop click. Native `closedby` light
// dismiss cannot be used because the dialog box covers the whole viewport.
const props = defineProps<{
  open: boolean;
  labelledby?: string;
  ariaLabel?: string;
  dialogClass?: string;
  // Blocks Esc and backdrop dismissal; only explicit close buttons close it.
  persistent?: boolean;
}>();

const emit = defineEmits<{
  (e: "update:open", value: boolean): void;
  (e: "close"): void;
}>();

const dialogRef = ref<HTMLDialogElement | null>(null);
let pressedOnBackdrop = false;

function sync(open: boolean) {
  const el = dialogRef.value;
  if (!el) return;
  if (open && !el.open) el.showModal();
  else if (!open && el.open) el.close();
}

// flush: "post" so slot content (and its autofocus targets) is rendered
// before showModal() runs its focusing steps.
watch(() => props.open, sync, { flush: "post" });
onMounted(() => sync(props.open));

// Fired for Esc, backdrop clicks and programmatic close(). Only propagate when
// the parent still thinks the modal is open.
function onClose() {
  if (props.persistent && props.open) {
    // Closed without a preventable `cancel` (e.g. Chrome's repeated-Esc quirk)
    // while the parent still wants it open: re-show instead of propagating.
    dialogRef.value?.showModal();
    return;
  }
  if (props.open) {
    emit("update:open", false);
    emit("close");
  }
}

function onCancel(e: Event) {
  if (props.persistent) e.preventDefault();
}

function onBackdropDown(e: MouseEvent) {
  pressedOnBackdrop = e.target === dialogRef.value;
}

// Close only when both press and release (the click's target) were on the
// backdrop, so a text-selection drag ending outside the content does not close.
function onBackdropClick(e: MouseEvent) {
  const pressed = pressedOnBackdrop;
  pressedOnBackdrop = false;
  if (props.persistent) return;
  if (pressed && e.target === dialogRef.value) {
    dialogRef.value?.close();
  }
}
</script>

<template>
  <dialog
    ref="dialogRef"
    class="modal"
    :class="dialogClass"
    :aria-labelledby="labelledby"
    :aria-label="labelledby ? undefined : ariaLabel"
    @cancel="onCancel"
    @close="onClose"
    @mousedown="onBackdropDown"
    @click="onBackdropClick"
  >
    <slot v-if="open" />
  </dialog>
</template>
