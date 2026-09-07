import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ref, nextTick } from "vue";
import { useChatScroll, BOTTOM_THRESHOLD } from "./useChatScroll";
import type { ChatMessage } from "../types";

describe("useChatScroll", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("exports BOTTOM_THRESHOLD and initializes with default values", () => {
    expect(BOTTOM_THRESHOLD).toBe(120);

    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, showScrollBottom, hasNewMessages } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    expect(scrollContainerRef.value).toBeNull();
    expect(showScrollBottom.value).toBe(false);
    expect(hasNewMessages.value).toBe(false);
  });

  it("updates showScrollBottom and resets hasNewMessages when scrolled to/away from bottom", () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);
    const onUpdateDetailsOpen = vi.fn<(open: boolean) => void>();

    const { scrollContainerRef, showScrollBottom, hasNewMessages, checkScrollPosition } =
      useChatScroll({
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

    // Away from bottom: scrollTop = 0, distanceFromBottom = 1000 - 0 - 500 = 500 > 120
    checkScrollPosition();
    expect(showScrollBottom.value).toBe(true);

    // Simulate having new messages while away from bottom
    hasNewMessages.value = true;

    // Near bottom: scrollTop = 450, distanceFromBottom = 1000 - 450 - 500 = 50 <= 120
    (mockElement as any).scrollTop = 450;
    checkScrollPosition();
    expect(showScrollBottom.value).toBe(false);
    expect(hasNewMessages.value).toBe(false);
  });

  it("scrolls to bottom with auto behavior on initial load and message updates when at bottom", async () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, hasNewMessages } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    const scrollToMock = vi.fn<(options?: ScrollToOptions) => void>();
    scrollContainerRef.value = {
      scrollTop: 500,
      scrollHeight: 1000,
      clientHeight: 500,
      scrollTo: scrollToMock,
    } as unknown as HTMLDivElement;

    // Initial sessionId watcher settles
    await nextTick();
    expect(scrollToMock).toHaveBeenCalledWith({
      top: 1000,
      behavior: "auto",
    });
    expect(hasNewMessages.value).toBe(false);

    scrollToMock.mockClear();

    // Trigger message update while at bottom
    messages.value = [{ id: "1", role: "user", content: "hello", timestamp: Date.now() }];
    await nextTick();
    await nextTick();

    expect(scrollToMock).toHaveBeenCalledWith({
      top: 1000,
      behavior: "auto",
    });
    expect(hasNewMessages.value).toBe(false);
  });

  it("maintains auto-sticking to bottom during rapid consecutive stream updates", async () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, hasNewMessages } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    const el = {
      scrollTop: 500,
      scrollHeight: 1000,
      clientHeight: 500,
      scrollTo: vi.fn<(o?: ScrollToOptions) => void>((o?: ScrollToOptions) => {
        if (o && typeof o.top === "number") el.scrollTop = o.top;
      }),
    };
    scrollContainerRef.value = el as unknown as HTMLDivElement;
    await nextTick();

    // Round 1
    el.scrollHeight += 200;
    messages.value = [{ id: "1", role: "assistant", content: "chunk 1", timestamp: Date.now() }];
    await nextTick();
    await nextTick();
    expect(el.scrollTo).toHaveBeenLastCalledWith({
      top: 1200,
      behavior: "auto",
    });
    expect(hasNewMessages.value).toBe(false);

    // Round 2
    el.scrollHeight += 200;
    messages.value = [
      { id: "1", role: "assistant", content: "chunk 1 and 2", timestamp: Date.now() },
    ];
    await nextTick();
    await nextTick();
    expect(el.scrollTo).toHaveBeenLastCalledWith({
      top: 1400,
      behavior: "auto",
    });
    expect(hasNewMessages.value).toBe(false);

    // Round 3
    el.scrollHeight += 200;
    messages.value = [
      { id: "1", role: "assistant", content: "chunk 1, 2 and 3", timestamp: Date.now() },
    ];
    await nextTick();
    await nextTick();
    expect(el.scrollTo).toHaveBeenLastCalledWith({
      top: 1600,
      behavior: "auto",
    });
    expect(hasNewMessages.value).toBe(false);
  });

  it("preserves viewport position and flags hasNewMessages when messages update away from bottom", async () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, hasNewMessages } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    const scrollToMock = vi.fn<(options?: ScrollToOptions) => void>();
    scrollContainerRef.value = {
      scrollTop: 0,
      scrollHeight: 1000,
      clientHeight: 500,
      scrollTo: scrollToMock,
    } as unknown as HTMLDivElement;

    // Settle initial sessionId watcher
    await nextTick();
    scrollToMock.mockClear();

    // User is scrolled to top (distanceFromBottom = 500 > 120)
    messages.value = [
      { id: "1", role: "assistant", content: "new incoming message", timestamp: Date.now() },
    ];
    await nextTick();

    expect(scrollToMock).not.toHaveBeenCalled();
    expect(hasNewMessages.value).toBe(true);
  });

  it("resets hasNewMessages and smoothly scrolls on scrollToBottom call", () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, hasNewMessages, scrollToBottom } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    const scrollToMock = vi.fn<(options?: ScrollToOptions) => void>();
    scrollContainerRef.value = {
      scrollHeight: 1200,
      scrollTo: scrollToMock,
    } as unknown as HTMLDivElement;

    hasNewMessages.value = true;

    scrollToBottom();

    expect(hasNewMessages.value).toBe(false);
    expect(scrollToMock).toHaveBeenCalledWith({
      top: 1200,
      behavior: "smooth",
    });
  });

  it("resets hasNewMessages and scrolls to bottom on session switch", async () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);

    const { scrollContainerRef, hasNewMessages } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
    });

    const scrollToMock = vi.fn<(options?: ScrollToOptions) => void>();
    scrollContainerRef.value = {
      scrollTop: 0,
      scrollHeight: 1000,
      clientHeight: 500,
      scrollTo: scrollToMock,
    } as unknown as HTMLDivElement;

    // Settle initial sessionId watcher
    await nextTick();
    scrollToMock.mockClear();

    hasNewMessages.value = true;

    sessionId.value = "sess-2";
    messages.value = [];
    await nextTick();
    await nextTick();

    expect(hasNewMessages.value).toBe(false);
    expect(scrollToMock).toHaveBeenCalledWith({
      top: 1000,
      behavior: "auto",
    });
  });

  it("triggers delayed re-check after 150ms timer expires", async () => {
    const messages = ref<ChatMessage[]>([]);
    const sessionId = ref<string | null>("sess-1");
    const isDetailsOpen = ref<boolean | undefined>(true);
    const onUpdateDetailsOpen = vi.fn<(open: boolean) => void>();

    const { scrollContainerRef, showScrollBottom } = useChatScroll({
      messages,
      sessionId,
      isDetailsOpen,
      onUpdateDetailsOpen,
    });

    const el = {
      scrollTop: 0,
      scrollHeight: 1000,
      clientHeight: 500,
      scrollTo: vi.fn<(o?: ScrollToOptions) => void>(),
    };
    scrollContainerRef.value = el as unknown as HTMLDivElement;

    // Trigger message update while at bottom (scrollTop at 500)
    el.scrollTop = 500;
    messages.value = [{ id: "1", role: "user", content: "hello", timestamp: Date.now() }];
    await nextTick();
    await nextTick();

    // Now simulate height expansion occurring asynchronously after images/DOM render
    el.scrollHeight = 2000;
    // Before timer fires, showScrollBottom hasn't refreshed for the async layout change
    vi.advanceTimersByTime(150);

    // After 150ms recheck timer, showScrollBottom is updated based on new scrollHeight
    expect(showScrollBottom.value).toBe(true);
  });

  describe("scrollToPrevUserMessage", () => {
    const makeNode = (top: number, bottom: number) => ({
      getBoundingClientRect: () => ({ top, bottom }),
    });

    const setup = (nodes: Array<ReturnType<typeof makeNode>>, scrollTop = 1000) => {
      const messages = ref<ChatMessage[]>([]);
      const sessionId = ref<string | null>("sess-1");
      const isDetailsOpen = ref<boolean | undefined>(true);

      const result = useChatScroll({
        messages,
        sessionId,
        isDetailsOpen,
      });

      const el = {
        scrollTop,
        scrollHeight: 2500,
        clientHeight: 500,
        scrollTo: vi.fn<(o?: ScrollToOptions) => void>(),
        getBoundingClientRect: () => ({ top: 0 }),
        querySelectorAll: () => nodes,
      };
      result.scrollContainerRef.value = el as unknown as HTMLDivElement;
      return { ...result, el };
    };

    it("targets the last user message entirely above the viewport, skipping visible ones", () => {
      // u1 fully visible, u2 partially visible at top, u3 entirely above
      const nodes = [makeNode(300, 400), makeNode(-50, 50), makeNode(-300, -200)];
      const { el, checkScrollPosition, showPrevUserMessage, scrollToPrevUserMessage } =
        setup(nodes);

      checkScrollPosition();
      expect(showPrevUserMessage.value).toBe(true);

      scrollToPrevUserMessage();
      expect(el.scrollTo).toHaveBeenCalledWith({
        top: 684, // (-300 + 1000) - 16 offset
        behavior: "smooth",
      });
    });

    it("hides and does nothing when no user message is above the viewport", () => {
      const nodes = [makeNode(300, 400), makeNode(100, 200)];
      const { el, checkScrollPosition, showPrevUserMessage, scrollToPrevUserMessage } =
        setup(nodes);

      checkScrollPosition();
      expect(showPrevUserMessage.value).toBe(false);

      el.scrollTo.mockClear();
      scrollToPrevUserMessage();
      expect(el.scrollTo).not.toHaveBeenCalled();
    });

    it("clamps scroll target to 0 when the previous user message is near the top", () => {
      const nodes = [makeNode(300, 400), makeNode(-995, -900)];
      const { el, checkScrollPosition, scrollToPrevUserMessage } = setup(nodes);

      checkScrollPosition();
      scrollToPrevUserMessage();
      expect(el.scrollTo).toHaveBeenCalledWith({ top: 0, behavior: "smooth" });
    });
  });
});
