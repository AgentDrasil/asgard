// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createApp, h, nextTick } from "vue";
import WelcomeScreen from "./WelcomeScreen.vue";
import NewChatView from "../views/NewChatView.vue";
import * as api from "../lib/api";
import * as toastModule from "../composables/useToast";
import type { AgentInfo } from "../types";
import { i18n } from "../i18n";

// Mock @iconify/vue
vi.mock("@iconify/vue", () => ({
  Icon: {
    name: "Icon",
    props: ["icon"],
    template: `<span class="mock-icon" :data-icon="icon"></span>`,
  },
}));

describe("WelcomeScreen.vue", () => {
  let root: HTMLElement;
  const mockToast = {
    error: vi.fn<(msg: string) => void>(),
    success: vi.fn<(msg: string) => void>(),
    warning: vi.fn<(msg: string) => void>(),
    info: vi.fn<(msg: string) => void>(),
  };

  const mockAgents: AgentInfo[] = [
    {
      id: "agent-1",
      name: "Test Agent",
      description: "A test coding agent",
      type: "agent",
      run_dirs: ["/workspace/project"],
      models: ["model-a", "model-b"],
      main_agent: true,
    },
  ];

  beforeEach(() => {
    root = document.createElement("div");
    document.body.appendChild(root);
    vi.restoreAllMocks();

    vi.spyOn(toastModule, "useToast").mockReturnValue(mockToast as any);
    vi.spyOn(api, "getDirInfo").mockResolvedValue({
      subdirs: ["src", "public"],
      gitRoot: "/workspace/project",
    });
    vi.spyOn(api, "getSubdirs").mockResolvedValue(["src", "public"]);
  });

  it("renders with default props and attaches files via input change", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const attachBtn = root.querySelector('button[title="Attach files"]') as HTMLButtonElement;
    expect(attachBtn).not.toBeNull();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).not.toBeNull();

    const file = new File(["dummy content"], "test-doc.pdf", { type: "application/pdf" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });

    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(root.textContent).toContain("test-doc.pdf");

    app.unmount();
  });

  it("handles clipboard paste with files", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const textarea = root.querySelector("textarea") as HTMLTextAreaElement;
    expect(textarea).not.toBeNull();

    const file = new File(["paste content"], "pasted-img.png", { type: "image/png" });
    const pasteEvent = new Event("paste", { bubbles: true, cancelable: true }) as any;
    pasteEvent.clipboardData = {
      files: [file],
    };

    textarea.dispatchEvent(pasteEvent);
    await nextTick();

    expect(root.textContent).toContain("pasted-img.png");

    app.unmount();
  });

  it("handles drag and drop files and ring highlight", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const card = root.querySelector(".max-w-2xl") as HTMLElement;
    expect(card).not.toBeNull();

    const dragEnterEvent = new Event("dragenter", { bubbles: true, cancelable: true }) as any;
    dragEnterEvent.dataTransfer = { types: ["Files"] };
    card.dispatchEvent(dragEnterEvent);
    await nextTick();

    expect(card.classList.contains("ring-2")).toBe(true);

    const file = new File(["drop content"], "dropped-data.csv", { type: "text/csv" });
    const dropEvent = new Event("drop", { bubbles: true, cancelable: true }) as any;
    dropEvent.dataTransfer = { files: [file] };

    card.dispatchEvent(dropEvent);
    await nextTick();

    expect(card.classList.contains("ring-2")).toBe(false);
    expect(root.textContent).toContain("dropped-data.csv");

    app.unmount();
  });

  it("removes attachment when clicking remove button", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["pdf"], "to-remove.txt", { type: "text/plain" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(root.textContent).toContain("to-remove.txt");

    const removeBtn = root.querySelector('button[title="Remove attachment"]') as HTMLButtonElement;
    expect(removeBtn).not.toBeNull();
    removeBtn.click();
    await nextTick();

    expect(root.textContent).not.toContain("to-remove.txt");

    app.unmount();
  });

  it("validates file size over 20MB", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const largeFile = new File(["a"], "large-file.bin");
    Object.defineProperty(largeFile, "size", { value: 25 * 1024 * 1024 });

    Object.defineProperty(fileInput, "files", {
      value: [largeFile],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(mockToast.error).toHaveBeenCalledWith('File "large-file.bin" exceeds 20MB limit');
    expect(root.textContent).not.toContain("large-file.bin");

    app.unmount();
  });

  it("validates file count over 20 attachments", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const files = Array.from({ length: 21 }, (_, i) => new File(["a"], `file-${i}.txt`));

    Object.defineProperty(fileInput, "files", {
      value: files,
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(mockToast.error).toHaveBeenCalledWith("Maximum 20 attachments allowed");
    expect(root.querySelectorAll(".bg-primary-500\\/10")).toHaveLength(0);

    app.unmount();
  });

  it("validates total file size over 50MB", async () => {
    mockToast.error.mockClear();
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file1 = new File(["a"], "file1.bin");
    Object.defineProperty(file1, "size", { value: 18 * 1024 * 1024 });
    const file2 = new File(["b"], "file2.bin");
    Object.defineProperty(file2, "size", { value: 18 * 1024 * 1024 });
    const file3 = new File(["c"], "file3.bin");
    Object.defineProperty(file3, "size", { value: 18 * 1024 * 1024 });

    Object.defineProperty(fileInput, "files", {
      value: [file1, file2, file3],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(mockToast.error).toHaveBeenCalledWith("Total attachment size exceeds 50MB limit");

    app.unmount();
  });

  it("deduplicates identical files by name and size", async () => {
    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Build something",
          loading: false,
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file1 = new File(["a"], "dup.txt");
    Object.defineProperty(file1, "size", { value: 100 });
    const file2 = new File(["b"], "dup.txt");
    Object.defineProperty(file2, "size", { value: 100 });

    Object.defineProperty(fileInput, "files", {
      value: [file1, file2],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    expect(root.textContent).toContain("dup.txt");
    const chips = root.querySelectorAll('button[title^="Remove "]');
    expect(chips).toHaveLength(1);

    app.unmount();
  });

  it("submits prompt with attached files and clears files after submit", async () => {
    let submittedFiles: File[] | undefined = undefined;

    const app = createApp({
      render() {
        return h(WelcomeScreen, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Start task",
          loading: false,
          onSubmit: (files?: File[]) => {
            submittedFiles = files;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["hello"], "hello.txt", { type: "text/plain" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    const submitBtn = root.querySelector("button.btn-primary") as HTMLButtonElement;
    submitBtn.click();
    await nextTick();

    expect(submittedFiles).toEqual([file]);
    expect(root.textContent).not.toContain("hello.txt");

    app.unmount();
  });

  it("NewChatView passes submit files payload upwards", async () => {
    let submittedFiles: File[] | undefined = undefined;

    const app = createApp({
      render() {
        return h(NewChatView, {
          agents: mockAgents,
          selectedAgentId: "agent-1",
          selectedDir: "/workspace/project",
          prompt: "Start task from NewChatView",
          loading: false,
          onSubmit: (files?: File[]) => {
            submittedFiles = files;
          },
        });
      },
    });
    app.use(i18n);
    app.mount(root);
    await nextTick();

    const fileInput = root.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(["payload"], "payload.png", { type: "image/png" });
    Object.defineProperty(fileInput, "files", {
      value: [file],
      writable: true,
    });
    fileInput.dispatchEvent(new Event("change"));
    await nextTick();

    const submitBtn = root.querySelector("button.btn-primary") as HTMLButtonElement;
    submitBtn.click();
    await nextTick();

    expect(submittedFiles).toEqual([file]);

    app.unmount();
  });
});
