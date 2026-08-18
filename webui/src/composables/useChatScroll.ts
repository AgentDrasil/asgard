import { ref, watch, nextTick, onMounted, onUnmounted, type Ref } from "vue";
import type { ChatMessage } from "../types";

export interface UseChatScrollOptions {
  messages: Ref<ChatMessage[]>;
  sessionId: Ref<string | null | undefined>;
  isDetailsOpen: Ref<boolean | undefined>;
  onUpdateDetailsOpen?: (open: boolean) => void;
}

export function useChatScroll(options: UseChatScrollOptions) {
  const { messages, sessionId, isDetailsOpen, onUpdateDetailsOpen } = options;

  const bottomRef = ref<HTMLDivElement | null>(null);
  const scrollContainerRef = ref<HTMLDivElement | null>(null);
  const showScrollBottom = ref(false);

  let lastAtTopState = isDetailsOpen.value ?? true;
  let ticking = false;

  const checkScrollPosition = () => {
    if (!scrollContainerRef.value) return;
    const el = scrollContainerRef.value;
    const atTop = el.scrollTop <= 5;
    if (atTop !== lastAtTopState) {
      lastAtTopState = atTop;
      onUpdateDetailsOpen?.(atTop);
    }
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    showScrollBottom.value = distanceFromBottom > 120;
  };

  const scrollToBottom = () => {
    bottomRef.value?.scrollIntoView({ behavior: "smooth" });
  };

  const handleScroll = () => {
    if (!ticking) {
      requestAnimationFrame(() => {
        checkScrollPosition();
        ticking = false;
      });
      ticking = true;
    }
  };

  watch(
    () => isDetailsOpen.value,
    (newVal) => {
      if (newVal !== undefined) {
        lastAtTopState = newVal;
      }
    },
  );

  onMounted(() => {
    const el = scrollContainerRef.value;
    if (el) {
      el.addEventListener("scroll", handleScroll, { passive: true });
      nextTick(() => {
        checkScrollPosition();
      });
    }
  });

  onUnmounted(() => {
    const el = scrollContainerRef.value;
    if (el) {
      el.removeEventListener("scroll", handleScroll);
    }
  });

  // Auto scroll to bottom when new messages arrive and re-check scroll position
  watch(
    [sessionId, messages],
    async () => {
      await nextTick();
      bottomRef.value?.scrollIntoView({ behavior: "smooth" });
      checkScrollPosition();
      setTimeout(() => {
        checkScrollPosition();
      }, 150);
    },
    { deep: true, immediate: true },
  );

  return {
    bottomRef,
    scrollContainerRef,
    showScrollBottom,
    scrollToBottom,
    checkScrollPosition,
  };
}
