import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";
import { useChatScroll } from "./useChatScroll";
import type { ChatMessage } from "../types";

describe("useChatScroll", () => {
  it("initializes with default values", () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, showScrollBottom } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    expect(scrollContainerRef.value).toBeNull();
    expect(showScrollBottom.value).toBe(false);
  });

  it("updates showScrollBottom when scrolled away from bottom", () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);
    const onUpdateDetailsOpen = vi.fn<(open: boolean) => void>();

    const { scrollContainerRef, showScrollBottom, checkScrollPosition } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
      onUpdateDetailsOpen,
    });

    const mockElement = {
      scrollTop: 0,
      scrollHeight: 1000,
      clientHeight: 500,
      scrollTo: vi.fn<(options?: ScrollToOptions) => void>(),
    } as unknown as HTMLDivElement;

    scrollContainerRef.value = mockElement;

    // scrollTop: 0, distanceFromBottom = 1000 - 0 - 500 = 500 > 120
    checkScrollPosition();
    expect(showScrollBottom.value).toBe(true);

    // Near bottom: scrollTop = 450, distanceFromBottom = 1000 - 450 - 500 = 50 <= 120
    (mockElement as any).scrollTop = 450;
    checkScrollPosition();
    expect(showScrollBottom.value).toBe(false);
  });

  it("scrolls container to bottom on scrollToBottom", () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, scrollToBottom } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    const scrollToMock = vi.fn<(options?: ScrollToOptions) => void>();
    scrollContainerRef.value = {
      scrollHeight: 1200,
      scrollTo: scrollToMock,
    } as unknown as HTMLDivElement;

    scrollToBottom();
    expect(scrollToMock).toHaveBeenCalledWith({
      top: 1200,
      behavior: "smooth",
    });
  });
});
