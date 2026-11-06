// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import DiffView from "./DiffView.vue";
import { i18n } from "../i18n";
import * as api from "../lib/api";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

vi.mock("@git-diff-view/vue", () => ({
  DiffView: {
    name: "MockDiffView",
    props: ["diffFile"],
    template: `<div class="mock-diff-view">{{ diffFile?.newPath }}</div>`,
  },
  DiffModeEnum: { Split: 1, Unified: 2 },
  SplitSide: { old: 1, new: 2 },
  DiffFile: class MockDiffFile {
    constructor() {}
    init() {}
    buildSplitDiffLines() {}
    buildUnifiedDiffLines() {}
  },
}));

vi.mock("vue-router", () => ({
  useRouter: () => ({
    push: vi.fn<() => void>(),
    replace: vi.fn<() => void>(),
  }),
  useRoute: () => ({
    path: "/chat/session-123/vcs",
    params: { id: "session-123" },
  }),
}));

describe("DiffView.vue error handling", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();

    const mockStorage: Record<string, string> = {};
    const storageMock: Storage = {
      getItem: (key: string) => mockStorage[key] || null,
      setItem: (key: string, value: string) => {
        mockStorage[key] = value;
      },
      removeItem: (key: string) => {
        delete mockStorage[key];
      },
      clear: () => {
        Object.keys(mockStorage).forEach((k) => delete mockStorage[k]);
      },
      key: (index: number) => Object.keys(mockStorage)[index] || null,
      length: 0,
    };
    Object.defineProperty(window, "localStorage", {
      value: storageMock,
      writable: true,
      configurable: true,
    });
  });

  it("displays server error message when loading git data fails", async () => {
    const errorMsg =
      'access denied: repository config "core.fsmonitor" is not allowed on the runner';
    vi.spyOn(api, "getGitLog").mockRejectedValue(new Error(errorMsg));
    vi.spyOn(api, "getGitDiff").mockResolvedValue([]);

    const app = createApp({
      render() {
        return h(DiffView, {
          sessionId: "session-123",
          runDir: "/workspace/Note",
          gitRoot: "/workspace/Note",
          isTerminalOpen: false,
          selectedCommit: null,
          selectedFilePath: null,
          chatInputText: "",
          isVCSSidebarOpen: true,
        });
      },
    });

    app.use(i18n);
    app.mount(root);
    await nextTick();
    await new Promise((r) => setTimeout(r, 20));
    await nextTick();

    const alertEl = root.querySelector(".alert-error");
    expect(alertEl).not.toBeNull();
    expect(alertEl?.textContent).toContain(errorMsg);

    // Verify retry button exists
    const retryBtn = root.querySelector(".btn-outline");
    expect(retryBtn).not.toBeNull();

    app.unmount();
  });
});
