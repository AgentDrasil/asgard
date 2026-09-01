import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";
import { useInPageFind } from "./useInPageFind";

describe("useInPageFind", () => {
  it("initializes with default closed state", () => {
    const containerRef = ref<HTMLElement | null>(null);
    const find = useInPageFind(containerRef);

    expect(find.isOpen.value).toBe(false);
    expect(find.query.value).toBe("");
    expect(find.currentIndex.value).toBe(0);
    expect(find.totalMatches.value).toBe(0);
    expect(find.matches.value).toEqual([]);
  });

  it("opens, closes and toggles find state", () => {
    const onOpen = vi.fn<() => void>();
    const onClose = vi.fn<() => void>();
    const containerRef = ref<HTMLElement | null>(null);
    const find = useInPageFind(containerRef, { onOpen, onClose });

    find.open();
    expect(find.isOpen.value).toBe(true);
    expect(onOpen).toHaveBeenCalledTimes(1);

    find.toggle();
    expect(find.isOpen.value).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);

    find.toggle();
    expect(find.isOpen.value).toBe(true);
    expect(onOpen).toHaveBeenCalledTimes(2);

    find.close();
    expect(find.isOpen.value).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("navigates next and prev with circular wraparound", () => {
    const containerRef = ref<HTMLElement | null>(null);
    const find = useInPageFind(containerRef);

    // Mock 3 match elements
    const mockEl1 = {
      classList: {
        add: vi.fn<(tokens: string) => void>(),
        remove: vi.fn<(tokens: string) => void>(),
      },
      scrollIntoView: vi.fn<() => void>(),
    } as unknown as HTMLElement;
    const mockEl2 = {
      classList: {
        add: vi.fn<(tokens: string) => void>(),
        remove: vi.fn<(tokens: string) => void>(),
      },
      scrollIntoView: vi.fn<() => void>(),
    } as unknown as HTMLElement;
    const mockEl3 = {
      classList: {
        add: vi.fn<(tokens: string) => void>(),
        remove: vi.fn<(tokens: string) => void>(),
      },
      scrollIntoView: vi.fn<() => void>(),
    } as unknown as HTMLElement;

    find.matches.value = [mockEl1, mockEl2, mockEl3];
    find.totalMatches.value = 3;
    find.currentIndex.value = 0;

    // Next -> 1
    find.findNext();
    expect(find.currentIndex.value).toBe(1);
    expect(mockEl2.classList.add).toHaveBeenCalledWith("asgard-find-active");
    expect(mockEl2.scrollIntoView).toHaveBeenCalled();

    // Next -> 2
    find.findNext();
    expect(find.currentIndex.value).toBe(2);

    // Next -> 0 (wraparound)
    find.findNext();
    expect(find.currentIndex.value).toBe(0);

    // Prev -> 2 (wraparound)
    find.findPrev();
    expect(find.currentIndex.value).toBe(2);

    // Prev -> 1
    find.findPrev();
    expect(find.currentIndex.value).toBe(1);
  });

  it("clears highlights and resets counts on clearHighlights", () => {
    const mockMark = {
      parentNode: {
        insertBefore: vi.fn<() => void>(),
        removeChild: vi.fn<() => void>(),
      },
      firstChild: null,
    } as unknown as Element;
    const mockQuerySelectorAll = vi.fn<() => Element[]>().mockReturnValue([mockMark]);
    const mockNormalize = vi.fn<() => void>();
    const mockContainer = {
      querySelectorAll: mockQuerySelectorAll,
      normalize: mockNormalize,
    } as unknown as HTMLElement;

    const containerRef = ref<HTMLElement | null>(mockContainer);
    const find = useInPageFind(containerRef);

    find.totalMatches.value = 5;
    find.currentIndex.value = 2;
    find.matches.value = [{} as HTMLElement];

    find.clearHighlights();

    expect(find.totalMatches.value).toBe(0);
    expect(find.currentIndex.value).toBe(0);
    expect(find.matches.value).toEqual([]);
    expect(mockQuerySelectorAll).toHaveBeenCalledWith("mark.asgard-find-match");
    expect(mockNormalize).toHaveBeenCalled();
  });
});
