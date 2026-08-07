import { ref, computed, onMounted, onUnmounted } from "vue";
import type { ITheme } from "@xterm/xterm";
import { resolveTerminalTheme } from "../themes/terminal";

/**
 * Terminal theme strictly follows the active DaisyUI / global document data-theme.
 */
export function useTerminalTheme() {
  const docTheme = ref<string | null>(
    typeof document !== "undefined" ? document.documentElement.getAttribute("data-theme") : null,
  );

  const activeTheme = computed<ITheme>(() => resolveTerminalTheme(docTheme.value));

  let observer: MutationObserver | null = null;
  onMounted(() => {
    observer = new MutationObserver(() => {
      docTheme.value = document.documentElement.getAttribute("data-theme");
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
  });

  onUnmounted(() => {
    observer?.disconnect();
    observer = null;
  });

  return { activeTheme };
}
