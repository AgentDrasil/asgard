<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from "vue";
import { Icon } from "@iconify/vue";
import { getCaretCoordinates } from "../../utils/caretCoordinates";
import { useProxyEnvs } from "../../composables/useProxyEnvs";

const props = defineProps<{
  targetElement: HTMLTextAreaElement | HTMLInputElement | null;
  modelValue: string;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: string): void;
  (e: "select", inserted: string): void;
}>();

const { proxyEnvs, load: loadProxyEnvs } = useProxyEnvs();

// State
const isOpen = ref(false);
const activeView = ref<"root" | "env">("root");
const selectedIndex = ref(0);
const slashIndex = ref<number>(-1);
const popupStyle = ref<{ top: string; left: string }>({ top: "0px", left: "0px" });
const popupRef = ref<HTMLDivElement | null>(null);

interface MenuItem {
  id: string;
  label: string;
  desc?: string;
  icon: string;
  action: () => void;
}

const rootItems = computed<MenuItem[]>(() => [
  {
    id: "env",
    label: "env",
    desc: "chat.slashEnvDesc",
    icon: "material-symbols:key-outline",
    action: () => {
      activeView.value = "env";
      selectedIndex.value = 0;
    },
  },
]);

const envItems = computed<MenuItem[]>(() => {
  if (proxyEnvs.value.length === 0) {
    return [];
  }
  return proxyEnvs.value.map((envName) => ({
    id: `env-${envName}`,
    label: envName,
    icon: "material-symbols:vpn-key-rounded",
    action: () => {
      insertCommand(`/env:${envName} `);
    },
  }));
});

const currentItems = computed<MenuItem[]>(() => {
  if (activeView.value === "root") return rootItems.value;
  return envItems.value;
});

const updatePosition = () => {
  if (!props.targetElement) return;
  const el = props.targetElement;
  const pos = el.selectionStart ?? el.value.length;
  const coords = getCaretCoordinates(el, pos);
  const rect = el.getBoundingClientRect();

  // Position relative to viewport or nearest scroll container
  // We place it floating above or below the caret
  const viewportHeight = window.innerHeight;
  const caretAbsoluteTop = rect.top + coords.top;
  const caretAbsoluteLeft = Math.min(rect.left + coords.left, window.innerWidth - 260);

  // If near bottom, place above
  if (caretAbsoluteTop + 220 > viewportHeight && caretAbsoluteTop > 240) {
    popupStyle.value = {
      top: `${Math.max(10, caretAbsoluteTop - 210)}px`,
      left: `${Math.max(10, caretAbsoluteLeft)}px`,
    };
  } else {
    popupStyle.value = {
      top: `${caretAbsoluteTop + coords.height + 6}px`,
      left: `${Math.max(10, caretAbsoluteLeft)}px`,
    };
  }
};

const openMenu = async () => {
  await loadProxyEnvs();
  activeView.value = "root";
  selectedIndex.value = 0;
  isOpen.value = true;
  await nextTick();
  updatePosition();
};

const closeMenu = () => {
  isOpen.value = false;
  activeView.value = "root";
  selectedIndex.value = 0;
  slashIndex.value = -1;
};

const insertCommand = (inserted: string) => {
  if (!props.targetElement) return;
  const el = props.targetElement;
  const val = props.modelValue;
  const start = slashIndex.value >= 0 ? slashIndex.value : (el.selectionStart ?? val.length);
  const end = el.selectionEnd ?? start;

  // Replace from slashIndex to current selection end with inserted
  const before = val.substring(0, start);
  const after = val.substring(end);
  const newVal = before + inserted + after;

  emit("update:modelValue", newVal);
  emit("select", inserted);
  closeMenu();

  nextTick(() => {
    el.focus();
    const newCursorPos = start + inserted.length;
    el.setSelectionRange(newCursorPos, newCursorPos);
  });
};

const handleTargetKeyDown = (e: KeyboardEvent) => {
  if (!isOpen.value) {
    if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      // Check if typing /
      const el = props.targetElement;
      if (!el) return;
      const cursor = el.selectionStart ?? el.value.length;
      slashIndex.value = cursor;
      // Open on next tick after character / is input
      setTimeout(() => {
        openMenu();
      }, 10);
    }
    return;
  }

  // When open:
  if (e.key === "ArrowDown") {
    e.preventDefault();
    if (currentItems.value.length > 0) {
      selectedIndex.value = (selectedIndex.value + 1) % currentItems.value.length;
    }
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    if (currentItems.value.length > 0) {
      selectedIndex.value =
        (selectedIndex.value - 1 + currentItems.value.length) % currentItems.value.length;
    }
  } else if (e.key === "Enter" || e.key === "Tab") {
    if (
      currentItems.value.length > 0 &&
      selectedIndex.value >= 0 &&
      selectedIndex.value < currentItems.value.length
    ) {
      e.preventDefault();
      e.stopPropagation();
      currentItems.value[selectedIndex.value].action();
    } else {
      closeMenu();
    }
  } else if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    if (activeView.value === "env") {
      activeView.value = "root";
      selectedIndex.value = 0;
    } else {
      closeMenu();
    }
  } else if (e.key === "Backspace") {
    const el = props.targetElement;
    if (el && (el.selectionStart ?? 0) <= slashIndex.value) {
      closeMenu();
    }
  }
};

const handleWindowClick = (e: MouseEvent) => {
  if (!isOpen.value) return;
  if (
    popupRef.value &&
    !popupRef.value.contains(e.target as Node) &&
    e.target !== props.targetElement
  ) {
    closeMenu();
  }
};

const handleWindowScrollOrResize = () => {
  if (isOpen.value) {
    updatePosition();
  }
};

watch(
  () => props.targetElement,
  (newEl, oldEl) => {
    if (oldEl) {
      oldEl.removeEventListener("keydown", handleTargetKeyDown as EventListener);
    }
    if (newEl) {
      newEl.addEventListener("keydown", handleTargetKeyDown as EventListener);
    }
  },
  { immediate: true },
);

onMounted(() => {
  window.addEventListener("click", handleWindowClick, true);
  window.addEventListener("resize", handleWindowScrollOrResize);
  window.addEventListener("scroll", handleWindowScrollOrResize, true);
});

onBeforeUnmount(() => {
  if (props.targetElement) {
    props.targetElement.removeEventListener("keydown", handleTargetKeyDown as EventListener);
  }
  window.removeEventListener("click", handleWindowClick, true);
  window.removeEventListener("resize", handleWindowScrollOrResize);
  window.removeEventListener("scroll", handleWindowScrollOrResize, true);
});

defineExpose({
  isOpen,
  closeMenu,
  openMenu,
});
</script>

<template>
  <Teleport to="body">
    <div
      v-if="isOpen"
      ref="popupRef"
      class="fixed z-9999 w-64 bg-base-100 border border-base-300 rounded-xl shadow-2xl py-1.5 px-1 text-base-content select-none overflow-hidden animate-in fade-in zoom-in-95 duration-100"
      :style="popupStyle"
      data-testid="slash-command-menu"
      @mousedown.prevent
    >
      <!-- Header / Breadcrumb -->
      <div
        class="flex items-center justify-between px-2.5 py-1 mb-1 border-b border-base-200/80 text-[11px] font-semibold text-base-content/60"
      >
        <div class="flex items-center gap-1">
          <span
            v-if="activeView === 'env'"
            class="cursor-pointer hover:text-primary flex items-center gap-0.5"
            @click="activeView = 'root'"
          >
            <Icon icon="material-symbols:chevron-left" class="w-3.5 h-3.5" />
            <span>/</span>
          </span>
          <span v-else>/</span>
          <span v-if="activeView === 'env'">env</span>
        </div>
        <span class="text-[10px] font-normal text-base-content/40">Esc</span>
      </div>

      <!-- List of options -->
      <div class="max-h-48 overflow-y-auto space-y-0.5 custom-scrollbar">
        <template v-if="activeView === 'root'">
          <div
            v-for="(item, idx) in rootItems"
            :key="item.id"
            @mouseenter="selectedIndex = idx"
            @click="item.action()"
            class="flex items-center justify-between px-2.5 py-1.5 rounded-lg text-xs cursor-pointer transition-colors"
            :class="
              idx === selectedIndex
                ? 'bg-primary text-primary-content font-medium'
                : 'hover:bg-base-200 text-base-content'
            "
          >
            <div class="flex items-center gap-2 min-w-0">
              <Icon :icon="item.icon" class="w-4 h-4 shrink-0" />
              <div class="flex flex-col min-w-0">
                <span class="font-mono">{{ item.label }}</span>
                <span v-if="item.desc" class="text-[10px] opacity-75 truncate max-w-[170px]">
                  {{ $t(item.desc) }}
                </span>
              </div>
            </div>
            <Icon icon="material-symbols:chevron-right" class="w-4 h-4 shrink-0 opacity-60" />
          </div>
        </template>

        <template v-else-if="activeView === 'env'">
          <div
            v-if="envItems.length === 0"
            class="px-3 py-3 text-center text-xs text-base-content/50 italic"
          >
            {{ $t("chat.slashNoEnvs") }}
          </div>
          <div
            v-for="(item, idx) in envItems"
            :key="item.id"
            @mouseenter="selectedIndex = idx"
            @click="item.action()"
            class="flex items-center gap-2 px-2.5 py-1.5 rounded-lg text-xs cursor-pointer transition-colors"
            :class="
              idx === selectedIndex
                ? 'bg-primary text-primary-content font-medium'
                : 'hover:bg-base-200 text-base-content'
            "
          >
            <Icon :icon="item.icon" class="w-4 h-4 shrink-0" />
            <span class="font-mono truncate">{{ item.label }}</span>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>
