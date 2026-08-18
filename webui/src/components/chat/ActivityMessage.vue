<script setup lang="ts">
import { Icon } from "@iconify/vue";
import type { ChatMessage, AgentInfo } from "../../types";
import { TOOL_ITEM_DELIMITER, getMessageArtifactFiles } from "../../utils/messageUtils";
import { getAgentIcon, formatPath } from "../../utils/agentUtils";

defineProps<{
  message: ChatMessage;
  activeAgent: AgentInfo | null;
  agents?: AgentInfo[];
}>();

const emit = defineEmits<{
  (e: "open-artifact", file: string): void;
}>();
</script>

<template>
  <!-- Reasoning / Thinking Balloon -->
  <div v-if="message.role === 'reasoning'" class="w-full sm:pl-2 sm:pr-12 my-2 min-w-0">
    <details
      open
      class="collapse collapse-arrow bg-base-200/50 border border-dashed border-base-300 rounded-lg min-w-0"
    >
      <summary
        class="collapse-title text-xs font-semibold text-base-content/65 cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none"
      >
        <span>💭</span> Thinking Process
      </summary>
      <div
        class="collapse-content text-xs font-mono text-base-content/50 whitespace-pre-wrap leading-relaxed break-words [word-break:break-word]"
      >
        {{ message.content }}
      </div>
    </details>
  </div>

  <!-- Error Message Card -->
  <div
    v-else-if="
      message.role === 'error' || (message.role === 'activity' && message.activityType === 'ERROR')
    "
    class="w-full pl-2 pr-2 my-2 min-w-0"
  >
    <div class="rounded-lg border border-error/40 bg-error/10 p-3 space-y-1.5 min-w-0">
      <div class="flex items-center gap-2 select-none min-w-0">
        <Icon icon="material-symbols:error-circle-rounded" class="h-4 w-4 text-error shrink-0" />
        <span class="text-xs font-bold text-error uppercase tracking-wider shrink-0"> Error </span>
        <span v-if="message.agentName" class="text-xs font-mono text-error/70 truncate min-w-0">
          {{ message.agentName }}
        </span>
      </div>
      <pre
        class="text-xs font-mono text-error/90 whitespace-pre-wrap break-words [word-break:break-word] min-w-0"
        >{{ message.content }}</pre>
    </div>
  </div>

  <!-- Activity / Step / Tool Call Collapsible Box -->
  <div
    v-else-if="
      message.role === 'activity' || message.role === 'tool_call' || message.role === 'tool_result'
    "
    class="w-full pl-2 pr-2 my-2 min-w-0"
  >
    <div class="flex items-center gap-2 mb-1.5 select-none">
      <Icon :icon="getAgentIcon(message.agentName, agents, activeAgent)" class="h-4 w-4 shrink-0" />
      <span class="text-xs font-bold text-base-content/70">
        {{ message.agentName || activeAgent?.name || "Agent" }}
      </span>
    </div>
    <details
      class="collapse collapse-arrow bg-base-200/40 border border-base-300 rounded-lg text-xs w-full min-w-0"
    >
      <summary
        class="collapse-title font-mono font-medium text-base-content/70 cursor-pointer py-2 min-h-0 flex items-center gap-2 select-none"
      >
        <span class="text-primary">⚙️</span>
        <span
          class="badge badge-sm badge-ghost text-[10px] uppercase tracking-wider font-semibold font-sans"
        >
          {{
            (message.activityType || message.role) === "TOOL_CALL" ||
            (message.activityType || message.role) === "tool_call" ||
            (message.activityType || message.role) === "tool_result"
              ? "TOOL"
              : message.activityType || message.role
          }}
        </span>
      </summary>
      <div class="collapse-content border-t border-base-300/40 pt-3 space-y-2 min-w-0">
        <!-- TargetFiles Artifact Card (click a file to open it in the artifact viewer) -->
        <div
          v-if="getMessageArtifactFiles(message).length > 0"
          class="p-2 rounded-lg bg-emerald-950/40 border border-emerald-800/60 mb-2 space-y-1.5"
        >
          <div class="text-emerald-400 font-bold text-xs select-none">
            📄 Target File{{ getMessageArtifactFiles(message).length > 1 ? "s" : "" }}:
          </div>
          <div class="flex flex-wrap gap-1.5">
            <button
              v-for="file in getMessageArtifactFiles(message)"
              :key="file"
              @click="emit('open-artifact', file)"
              class="btn btn-xs gap-1.5 bg-emerald-600/80 hover:bg-emerald-500 text-white border-none font-mono normal-case h-6 min-h-0 px-2 max-w-full"
              :title="`Open artifact: ${file}`"
            >
              <Icon icon="octicon:file-code-24" class="h-3.5 w-3.5 shrink-0" />
              <span class="truncate max-w-[280px]">{{ formatPath(file) }}</span>
            </button>
          </div>
        </div>
        <template
          v-for="(item, idx) in message.content.includes(TOOL_ITEM_DELIMITER)
            ? message.content.split(TOOL_ITEM_DELIMITER)
            : message.content.split('\n\n')"
          :key="idx"
        >
          <pre
            v-if="item.trim()"
            class="bg-base-200/80 p-3 rounded-lg border border-base-300 overflow-x-auto max-w-full min-w-0 text-xs font-mono text-base-content/80"
          ><code class="whitespace-pre-wrap break-words [word-break:break-word]">{{ item.trim() }}</code></pre>
        </template>
      </div>
    </details>
  </div>
</template>
