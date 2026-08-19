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
});
