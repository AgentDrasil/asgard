<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import type { ChatSession, AgentInfo } from "../types";
import { Icon } from "@iconify/vue";
import { reloadAgents, restartServer, getSystemStatus } from "../lib/api";
import SessionList from "./sidebar/SessionList.vue";
import ThemeSelector from "./sidebar/ThemeSelector.vue";
import { useShortcuts } from "../composables/useShortcuts";
import { useToast } from "../composables/useToast";

const { toggleSidebarShortcut, toggleTerminalShortcut } = useShortcuts();
const toast = useToast();

const props = withDefaults(
  defineProps<{
    sessions: ChatSession[];
    agents?: AgentInfo[];
    activeSessionId: string | null;
    isOpen?: boolean;
  }>(),
  {
    isOpen: true,
    agents: () => [],
  },
);

const emit = defineEmits<{
  (e: "select-session", id: string): void;
  (e: "new-chat", agentId?: string, runDir?: string): void;
  (e: "delete-session", id: string): void;
  (e: "toggle-sidebar"): void;
  (e: "toggle-terminal"): void;
  (e: "open-quota"): void;
  (e: "open-config"): void;
  (e: "reload-agents"): void;
}>();

const isReloading = ref(false);
const isRestarting = ref(false);
const isRestartConfirmOpen = ref(false);
const viewMode = ref<"list" | "agent">("list");

let restartAbortController: AbortController | null = null;

const toggleViewMode = (mode: "list" | "agent") => {
  viewMode.value = mode;
  localStorage.setItem("asgard_sidebar_view_mode", mode);
};

// Resizable sidebar width logic
const DEFAULT_WIDTH = 256;
const MIN_WIDTH = 200;
const MAX_WIDTH = 500;

const sidebarWidth = ref(DEFAULT_WIDTH);
const isResizing = ref(false);

const startResize = (e: MouseEvent) => {
  e.preventDefault();
  isResizing.value = true;
  document.addEventListener("mousemove", handleMouseMove);
  document.addEventListener("mouseup", stopResize);
  document.body.style.userSelect = "none";
  document.body.style.cursor = "col-resize";
};

const handleMouseMove = (e: MouseEvent) => {
  if (!isResizing.value) return;
  const newWidth = Math.min(Math.max(e.clientX, MIN_WIDTH), MAX_WIDTH);
  sidebarWidth.value = newWidth;
};

const stopResize = () => {
  if (isResizing.value) {
    isResizing.value = false;
    localStorage.setItem("asgard_sidebar_width", sidebarWidth.value.toString());
    document.removeEventListener("mousemove", handleMouseMove);
    document.removeEventListener("mouseup", stopResize);
    document.body.style.userSelect = "";
    document.body.style.cursor = "";
  }
};

const reloadApp = async () => {
  if (isReloading.value) return;
  isReloading.value = true;
  try {
    const result = await reloadAgents();
    if (result.success) {
      toast.success("Agent 配置重载成功", { title: "重载成功 (Reload Success)" });
      emit("reload-agents");
    } else {
      toast.error(result.error || "Agent 配置重载失败", { title: "重载失败 (Reload Error)" });
    }
  } catch (err: any) {
    toast.error(err?.message || "Agent 配置重载失败", { title: "重载失败 (Reload Error)" });
  } finally {
    isReloading.value = false;
  }
};

const openRestartConfirm = () => {
  if (isRestarting.value) return;
  isRestartConfirmOpen.value = true;
};

const handleRestartModalKeydown = (e: KeyboardEvent) => {
  if (e.key === "Escape" && isRestartConfirmOpen.value && !isRestarting.value) {
    isRestartConfirmOpen.value = false;
  }
};

const triggerRestartWorkflow = async () => {
  isRestartConfirmOpen.value = false;
  if (isRestarting.value) return;
  isRestarting.value = true;

  // 1. Send restart signal
  const accepted = await restartServer();
  if (!accepted) {
    isRestarting.value = false;
    toast.error("重启请求被服务器拒绝 (HTTP error)，请检查后端日志。", {
      title: "Restart Failed",
    });
    return;
  }

  // 2. Poll /api/system/status with backoff and timeout (120s)
  toast.info("服务正在重启，页面将在服务就绪后自动刷新...", {
    title: "正在重启 (Restarting)",
    duration: 10000,
  });

  restartAbortController = new AbortController();
  const abortSignal = restartAbortController.signal;
  const startTime = Date.now();
  const timeoutMs = 120_000;
  const initialDelay = 1000;
  const interval = 1500;

  // Initial delay to give process time to exit
  await new Promise((resolve) => setTimeout(resolve, initialDelay));

  const pollStatus = async () => {
    while (Date.now() - startTime < timeoutMs) {
      if (abortSignal.aborted) return;
      const status = await getSystemStatus();
      if (status !== null) {
        if (abortSignal.aborted) return;
        // Server is back online!
        toast.success("服务已重新上线，正在刷新页面...", { title: "重启完成" });
        setTimeout(() => {
          if (!abortSignal.aborted) {
            window.location.reload();
          }
        }, 500);
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, interval));
    }

    if (abortSignal.aborted) return;

    // Timeout reached
    isRestarting.value = false;
    toast.error(
      "服务重启探测超时 (120s)。若容器未配置 restart 策略（如 --restart=always），请手动检查 Docker 容器状态 (docker ps / docker logs)。",
      { title: "重启超时 (Restart Timeout)", duration: 0 },
    );
  };

  void pollStatus();
};

defineExpose({
  openRestartConfirm,
  triggerRestartWorkflow,
});

onMounted(() => {
  window.addEventListener("keydown", handleRestartModalKeydown);
  const savedViewMode = localStorage.getItem("asgard_sidebar_view_mode");
  if (savedViewMode === "list" || savedViewMode === "agent") {
    viewMode.value = savedViewMode;
  }

  const savedWidth = localStorage.getItem("asgard_sidebar_width");
  if (savedWidth) {
    const parsed = parseInt(savedWidth, 10);
    if (!isNaN(parsed) && parsed >= MIN_WIDTH && parsed <= MAX_WIDTH) {
      sidebarWidth.value = parsed;
    }
  }
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleRestartModalKeydown);
  document.removeEventListener("mousemove", handleMouseMove);
  document.removeEventListener("mouseup", stopResize);
  if (restartAbortController) {
    restartAbortController.abort();
    restartAbortController = null;
  }
});
</script>

<template>
  <aside
    :style="isOpen ? { width: `${sidebarWidth}px` } : undefined"
    :class="[
      isOpen
        ? 'max-md:w-72 max-md:translate-x-0'
        : 'w-72 md:w-16 md:items-center max-md:-translate-x-full',
      'bg-base-300 border-r border-base-100 flex flex-col h-full shrink-0 relative',
      'max-md:fixed max-md:top-0 max-md:bottom-0 max-md:left-0 max-md:z-50 max-md:shadow-2xl md:relative',
      isResizing ? 'select-none transition-none' : 'transition-all duration-300',
    ]"
  >
    <!-- Resize Handle (Visible only when sidebar is open on desktop) -->
    <div
      v-if="isOpen"
      @mousedown="startResize"
      class="hidden md:block absolute right-0 top-0 bottom-0 w-1.5 cursor-col-resize hover:bg-primary/40 active:bg-primary z-30 transition-colors"
      title="Drag to resize sidebar"
    />

    <!-- Header / Toggle Sidebar Button -->
    <div
      :class="['p-4 flex items-center gap-2 w-full', isOpen ? 'justify-between' : 'justify-center']"
    >
      <h1
        v-if="isOpen"
        class="text-lg font-bold bg-gradient-to-r from-indigo-600 to-cyan-600 dark:from-indigo-400 dark:to-cyan-400 bg-clip-text text-transparent truncate"
      >
        Asgard
      </h1>
      <button
        @click="emit('toggle-sidebar')"
        class="btn btn-ghost btn-xs btn-square text-base-content/70 hover:text-base-content"
        :title="
          isOpen
            ? `Collapse Sidebar (${toggleSidebarShortcut})`
            : `Expand Sidebar (${toggleSidebarShortcut})`
        "
      >
        <Icon icon="mynaui:sidebar" class="h-5 w-5 fill-current" />
      </button>
    </div>

    <!-- New Chat Button (above view mode switch) -->
    <div :class="['px-2 pt-1 pb-1 w-full flex flex-col items-center']">
      <button
        @click="emit('new-chat')"
        :class="[
          'flex items-center gap-3 py-2.5 rounded-lg cursor-pointer transition-all duration-200 text-sm font-medium text-base-content/85 hover:bg-base-200',
          isOpen ? 'w-full px-3' : 'w-10 h-10 justify-center p-0',
        ]"
        title="New chat"
      >
        <Icon icon="mynaui:edit-one" class="h-5 w-5 fill-current" />
        <span v-if="isOpen">New chat</span>
      </button>
    </div>

    <!-- View Mode Switcher (List Mode vs Group by Agent Mode) -->
    <div v-if="isOpen" class="px-3 pb-2 w-full">
      <div class="join w-full bg-base-200/60 p-0.5 rounded-lg">
        <button
          @click="toggleViewMode('list')"
          :class="[
            'join-item btn btn-xs flex-1 border-none font-medium gap-1.5',
            viewMode === 'list'
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content',
          ]"
          title="List View Mode"
        >
          <Icon icon="mynaui:list-solid" class="h-4 w-4 fill-current" />
          <span>List</span>
        </button>
        <button
          @click="toggleViewMode('agent')"
          :class="[
            'join-item btn btn-xs flex-1 border-none font-medium gap-1.5',
            viewMode === 'agent'
              ? 'btn-primary shadow-xs'
              : 'btn-ghost text-base-content/70 hover:text-base-content',
          ]"
          title="Group by Agent & Workspace"
        >
          <Icon icon="mynaui:grid" class="h-4 w-4 fill-current" />
          <span>By Agent</span>
        </button>
      </div>
    </div>

    <!-- Sessions List -->
    <div class="flex-1 overflow-y-auto p-2 space-y-1 w-full flex flex-col items-center">
      <template v-if="isOpen">
        <SessionList
          :sessions="sessions"
          :agents="agents"
          :active-session-id="activeSessionId"
          :view-mode="viewMode"
          @select-session="emit('select-session', $event)"
          @delete-session="emit('delete-session', $event)"
          @new-chat="(agentId, dir) => emit('new-chat', agentId, dir)"
        />
      </template>
    </div>

    <!-- Action Menu (Adaptive flex-wrap layout, visible when sidebar is open) -->
    <div
      v-if="isOpen"
      class="px-2 py-1.5 flex flex-wrap items-center justify-around gap-1 w-full border-t border-base-100/50 bg-base-300"
    >
      <!-- 1. Quota -->
      <button
        @click="emit('open-quota')"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Check Quota"
        :disabled="isRestarting"
      >
        <Icon icon="mynaui:chart-bar-one" class="h-4.5 w-4.5 fill-current" />
      </button>

      <!-- 2. Theme Selector Dropdown -->
      <ThemeSelector />

      <!-- 3. Terminal -->
      <button
        @click="emit('toggle-terminal')"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        :title="`Toggle Global Terminal (${toggleTerminalShortcut})`"
        :disabled="isRestarting"
      >
        <Icon icon="mynaui:terminal" class="h-4.5 w-4.5 fill-current" />
      </button>

      <!-- 4. Refresh Agent -->
      <button
        @click="reloadApp"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Reload Agents"
        :disabled="isReloading || isRestarting"
      >
        <Icon
          icon="mynaui:refresh"
          :class="['h-4.5 w-4.5 fill-current', { 'animate-spin': isReloading }]"
        />
      </button>

      <!-- 5. Config Editor -->
      <button
        @click="emit('open-config')"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Configuration Editor"
        :disabled="isRestarting"
      >
        <Icon icon="mynaui:cog" class="h-4.5 w-4.5 fill-current" />
      </button>

      <!-- 6. Restart Server -->
      <button
        @click="isRestartConfirmOpen = true"
        class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
        title="Restart Server"
        :disabled="isRestarting"
      >
        <Icon
          icon="mynaui:power"
          :class="['h-4.5 w-4.5 fill-current text-error/80', { 'animate-spin': isRestarting }]"
        />
      </button>
    </div>

    <!-- Restart Confirmation Modal -->
    <Transition name="fade">
      <div
        v-if="isRestartConfirmOpen"
        class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-center justify-center p-4"
        @click.self="isRestartConfirmOpen = false"
      >
        <div
          class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-md p-6 shadow-2xl space-y-4"
        >
          <div class="flex items-start gap-3">
            <div class="p-2.5 rounded-full bg-warning/10 text-warning shrink-0">
              <Icon icon="mynaui:danger" class="h-6 w-6" />
            </div>
            <div class="space-y-1">
              <h3 class="font-bold text-lg text-base-content">确认重启后端服务？</h3>
              <p class="text-sm text-base-content/70 leading-relaxed">
                重启操作将安全终止当前 Asgard 后端进程。
              </p>
            </div>
          </div>

          <div
            class="bg-base-300/60 rounded-xl p-3.5 border border-base-100/40 text-xs text-base-content/80 space-y-1.5"
          >
            <div class="font-semibold text-warning flex items-center gap-1.5">
              <Icon icon="mynaui:info-triangle" class="h-4 w-4 shrink-0" />
              <span>重要提示 (Prerequisites)</span>
            </div>
            <p>
              请确保 Docker 容器启动时配置了自动重启策略（如 <code>--restart=always</code> 或
              <code>--restart=unless-stopped</code>），否则进程退出后容器将不会自动重新启动。
            </p>
            <p class="text-base-content/60 text-[11px]">
              重启过程中页面将自动轮询系统状态，恢复后将自动刷新。
            </p>
          </div>

          <div class="flex items-center justify-end gap-2 pt-2">
            <button
              @click="isRestartConfirmOpen = false"
              class="btn btn-ghost btn-sm"
              :disabled="isRestarting"
            >
              取消
            </button>
            <button
              @click="triggerRestartWorkflow"
              class="btn btn-error btn-sm gap-1.5"
              :disabled="isRestarting"
            >
              <Icon icon="mynaui:power" class="h-4 w-4" />
              <span>确认重启</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </aside>
</template>
