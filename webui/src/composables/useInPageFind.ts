import { ref, watch, onUnmounted, nextTick, getCurrentInstance, type Ref } from "vue";

export interface InPageFindOptions {
  /** Callback when find bar is opened */
  onOpen?: () => void;
  /** Callback when find bar is closed */
  onClose?: () => void;
}

export function useInPageFind(
  containerRef: Ref<HTMLElement | null>,
  options: InPageFindOptions = {},
) {
  const isOpen = ref(false);
  const query = ref("");
  const currentIndex = ref(0);
  const totalMatches = ref(0);
  const matches = ref<HTMLElement[]>([]);

  let debounceTimer: ReturnType<typeof setTimeout> | null = null;

  function escapeRegExp(str: string): string {
    return str.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  function clearHighlights() {
    if (!containerRef.value) return;
    const container = containerRef.value;
    const existingMarks = container.querySelectorAll("mark.asgard-find-match");
    if (existingMarks.length === 0) {
      matches.value = [];
      totalMatches.value = 0;
      currentIndex.value = 0;
      return;
    }
    existingMarks.forEach((mark) => {
      const parent = mark.parentNode;
      if (parent) {
        while (mark.firstChild) {
          parent.insertBefore(mark.firstChild, mark);
        }
        parent.removeChild(mark);
      }
    });
    container.normalize();
    matches.value = [];
    totalMatches.value = 0;
    currentIndex.value = 0;
  }

  function performSearch() {
    clearHighlights();
    const container = containerRef.value;
    const q = query.value.trim();
    if (!container || !q) {
      return;
    }

    const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, {
      acceptNode(node) {
        const parent = node.parentElement;
        if (!parent) return NodeFilter.FILTER_REJECT;

        const tag = parent.tagName.toUpperCase();
        if (
          tag === "SCRIPT" ||
          tag === "STYLE" ||
          tag === "TEXTAREA" ||
          tag === "INPUT" ||
          tag === "BUTTON"
        ) {
          return NodeFilter.FILTER_REJECT;
        }

        if (parent.closest(".find-bar-ignore") || parent.closest("mark.asgard-find-match")) {
          return NodeFilter.FILTER_REJECT;
        }

        if (!node.nodeValue || !node.nodeValue.trim()) {
          return NodeFilter.FILTER_SKIP;
        }

        return NodeFilter.FILTER_ACCEPT;
      },
    });

    const textNodes: Text[] = [];
    let currentNode = walker.nextNode();
    while (currentNode) {
      textNodes.push(currentNode as Text);
      currentNode = walker.nextNode();
    }

    const regex = new RegExp(escapeRegExp(q), "gi");
    const foundMarks: HTMLElement[] = [];

    for (const node of textNodes) {
      const text = node.nodeValue || "";
      regex.lastIndex = 0;

      if (!regex.test(text)) continue;

      regex.lastIndex = 0;
      const fragment = document.createDocumentFragment();
      let lastIndex = 0;
      let match: RegExpExecArray | null;

      while ((match = regex.exec(text)) !== null) {
        const matchStart = match.index;
        const matchEnd = regex.lastIndex;

        if (matchStart > lastIndex) {
          fragment.appendChild(document.createTextNode(text.substring(lastIndex, matchStart)));
        }

        const mark = document.createElement("mark");
        mark.className = "asgard-find-match";
        mark.textContent = text.substring(matchStart, matchEnd);
        fragment.appendChild(mark);
        foundMarks.push(mark);

        lastIndex = matchEnd;
      }

      if (lastIndex < text.length) {
        fragment.appendChild(document.createTextNode(text.substring(lastIndex)));
      }

      const parent = node.parentNode;
      if (parent) {
        parent.replaceChild(fragment, node);
      }
    }

    matches.value = foundMarks;
    totalMatches.value = foundMarks.length;

    if (foundMarks.length > 0) {
      currentIndex.value = 0;
      setActiveMatch(0);
    }
  }

  function setActiveMatch(index: number) {
    if (matches.value.length === 0) return;

    // Normalize index
    const targetIndex =
      ((index % matches.value.length) + matches.value.length) % matches.value.length;
    currentIndex.value = targetIndex;

    // Update active class
    matches.value.forEach((m, idx) => {
      if (idx === targetIndex) {
        m.classList.add("asgard-find-active");
      } else {
        m.classList.remove("asgard-find-active");
      }
    });

    // Scroll into view
    const targetEl = matches.value[targetIndex];
    if (targetEl) {
      targetEl.scrollIntoView({
        behavior: "smooth",
        block: "center",
        inline: "nearest",
      });
    }
  }

  function findNext() {
    if (totalMatches.value === 0) return;
    setActiveMatch(currentIndex.value + 1);
  }

  function findPrev() {
    if (totalMatches.value === 0) return;
    setActiveMatch(currentIndex.value - 1);
  }

  function open() {
    isOpen.value = true;
    options.onOpen?.();
    nextTick(() => {
      if (query.value.trim()) {
        performSearch();
      }
    });
  }

  function close() {
    isOpen.value = false;
    clearHighlights();
    options.onClose?.();
  }

  function toggle() {
    if (isOpen.value) {
      close();
    } else {
      open();
    }
  }

  // Watch query changes with a small debounce
  watch(query, (newVal) => {
    if (!isOpen.value) return;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      if (newVal.trim()) {
        performSearch();
      } else {
        clearHighlights();
      }
    }, 60);
  });

  if (getCurrentInstance()) {
    onUnmounted(() => {
      if (debounceTimer) clearTimeout(debounceTimer);
      clearHighlights();
    });
  }

  return {
    isOpen,
    query,
    currentIndex,
    totalMatches,
    matches,
    open,
    close,
    toggle,
    findNext,
    findPrev,
    performSearch,
    clearHighlights,
    setActiveMatch,
  };
}
