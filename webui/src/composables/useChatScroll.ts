import { ref, watch, nextTick, onMounted, onUnmounted, getCurrentInstance, type Ref } from "vue";
import type { ChatMessage } from "../types";

export const BOTTOM_THRESHOLD = 120;

const USER_MESSAGE_SELECTOR = "[data-user-message]";
// Element edges within this distance of the viewport top still count as visible
const VISIBLE_EPSILON = 1;
// Breathing room between viewport top and the message we jump to
const JUMP_TOP_OFFSET = 16;

export interface UseChatScrollOptions {
  messages: Ref<ChatMessage[]>;
  sessionId: Ref<string | null | undefined>;
  isDetailsOpen: Ref<boolean | undefined>;
  onUpdateDetailsOpen?: (open: boolean) => void;
}

export function useChatScroll(options: UseChatScrollOptions) {
  const { messages, sessionId, isDetailsOpen, onUpdateDetailsOpen } = options;

  const scrollContainerRef = ref<HTMLDivElement | null>(null);
  const showScrollBottom = ref(false);
  const showPrevUserMessage = ref(false);
  const hasNewMessages = ref(false);

  let lastAtTopState = isDetailsOpen.value ?? true;
  let ticking = false;
  let switchingSession = false;
  let recheckTimer: ReturnType<typeof setTimeout> | null = null;

  const scheduleRecheck = () => {
    if (recheckTimer !== null) {
      clearTimeout(recheckTimer);
    }
    recheckTimer = setTimeout(() => {
      recheckTimer = null;
      checkScrollPosition();
    }, 150);
  };

  const checkScrollPosition = () => {
    if (!scrollContainerRef.value) return;
    const el = scrollContainerRef.value;
    const atTop = el.scrollTop <= 5;
    if (atTop !== lastAtTopState) {
      lastAtTopState = atTop;
      onUpdateDetailsOpen?.(atTop);
    }
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    showScrollBottom.value = distanceFromBottom > BOTTOM_THRESHOLD;
    if (distanceFromBottom <= BOTTOM_THRESHOLD) {
      hasNewMessages.value = false;
    }
    updatePrevUserMessageState();
  };

  const getUserMessageNodes = (): HTMLElement[] => {
    const el = scrollContainerRef.value;
    if (!el || typeof el.querySelectorAll !== "function") return [];
    return Array.from(el.querySelectorAll<HTMLElement>(USER_MESSAGE_SELECTOR));
  };

  // Find the most recent user message entirely above the viewport, if any
  const findPrevUserMessage = (): HTMLElement | null => {
    const el = scrollContainerRef.value;
    if (!el || typeof el.getBoundingClientRect !== "function") return null;
    const containerTop = el.getBoundingClientRect().top;
    const nodes = getUserMessageNodes();
    for (let i = nodes.length - 1; i >= 0; i--) {
      const rect = nodes[i]!.getBoundingClientRect();
      const bottomAbs = rect.bottom - containerTop + el.scrollTop;
      if (bottomAbs <= el.scrollTop + VISIBLE_EPSILON) {
        return nodes[i]!;
      }
    }
    return null;
  };

  const updatePrevUserMessageState = () => {
    showPrevUserMessage.value = findPrevUserMessage() !== null;
  };

  const scrollToPrevUserMessage = () => {
    const el = scrollContainerRef.value;
    if (!el || typeof el.getBoundingClientRect !== "function") return;
    const containerTop = el.getBoundingClientRect().top;
    const target = findPrevUserMessage();
    if (!target) return;
    const rect = target.getBoundingClientRect();
    const topAbs = rect.top - containerTop + el.scrollTop;
    el.scrollTo({
      top: Math.max(0, topAbs - JUMP_TOP_OFFSET),
      behavior: "smooth",
    });
  };

  const stickToBottom = () => {
    if (scrollContainerRef.value) {
      scrollContainerRef.value.scrollTo({
        top: scrollContainerRef.value.scrollHeight,
        behavior: "auto",
      });
    }
  };

  const scrollToBottom = () => {
    if (scrollContainerRef.value) {
      scrollContainerRef.value.scrollTo({
        top: scrollContainerRef.value.scrollHeight,
        behavior: "smooth",
      });
    }
    hasNewMessages.value = false;
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

  if (getCurrentInstance()) {
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
      if (recheckTimer !== null) {
        clearTimeout(recheckTimer);
        recheckTimer = null;
      }
      const el = scrollContainerRef.value;
      if (el) {
        el.removeEventListener("scroll", handleScroll);
      }
    });
  }

  // 1. Session ID watcher: triggers on mount and session change
  watch(
    sessionId,
    async () => {
      switchingSession = true;
      hasNewMessages.value = false;
      try {
        await nextTick();
        stickToBottom();
        checkScrollPosition();
      } finally {
        switchingSession = false;
      }
      scheduleRecheck();
    },
    { immediate: true },
  );

  // 2. Messages watcher: auto-scroll if at bottom, otherwise mark hasNewMessages
  watch(
    messages,
    async () => {
      const el = scrollContainerRef.value;
      const isAtBottom =
        switchingSession ||
        !el ||
        el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_THRESHOLD;

      if (isAtBottom) {
        await nextTick();
        stickToBottom();
        checkScrollPosition();
        hasNewMessages.value = false;
        scheduleRecheck();
      } else {
        hasNewMessages.value = true;
      }
    },
    { deep: true },
  );

  return {
    scrollContainerRef,
    showScrollBottom,
    showPrevUserMessage,
    hasNewMessages,
    scrollToBottom,
    scrollToPrevUserMessage,
    checkScrollPosition,
  };
}
