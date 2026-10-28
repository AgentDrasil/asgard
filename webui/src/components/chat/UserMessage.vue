<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Icon } from "@iconify/vue";
import type { ChatMessage } from "../../types";
import { formatTimestamp, formatFileSize } from "../../lib/format";
import { getFileIcon } from "../../utils/fileUtils";
import { getAttachmentUrl, appendAccessToken } from "../../lib/api";
import { useUserProfile } from "../../composables/useUserProfile";

defineProps<{
  message: ChatMessage;
  sessionId?: string | null;
}>();

const { profile, load } = useUserProfile();
onMounted(load);
const avatarFailed = ref(false);

const displayName = computed(() => profile.value?.name?.trim() || "");
const avatarSrc = computed(() =>
  profile.value?.avatarUrl ? appendAccessToken(profile.value.avatarUrl) : "",
);
const initials = computed(() => {
  const parts = displayName.value.split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "";
  const letters =
    parts.length === 1
      ? Array.from(parts[0]).slice(0, 2)
      : parts.slice(0, 2).map((p) => Array.from(p)[0]);
  return letters.join("").toUpperCase();
});
</script>

<template>
  <div class="chat chat-end min-w-0">
    <div
      class="chat-header text-[10px] text-base-content/40 mb-1 select-none flex items-center gap-2 justify-end min-w-0"
    >
      <span v-if="message.timestamp" class="font-normal font-mono">
        {{ formatTimestamp(message.timestamp) }}
      </span>
      <span class="text-xs font-bold text-base-content/70 truncate" data-testid="user-name">
        {{ displayName || $t("chat.you") }}
      </span>
      <template v-if="displayName">
        <img
          v-if="avatarSrc && !avatarFailed"
          :src="avatarSrc"
          alt=""
          referrerpolicy="no-referrer"
          class="h-4 w-4 shrink-0 rounded-full object-cover"
          data-testid="user-avatar"
          @error="avatarFailed = true"
        />
        <span
          v-else
          class="h-4 w-4 shrink-0 rounded-full bg-primary/15 text-primary text-[8px] font-bold flex items-center justify-center"
          data-testid="user-initials"
        >
          {{ initials }}
        </span>
      </template>
    </div>
    <div
      class="chat-bubble chat-bubble-primary text-primary-content text-sm leading-relaxed max-w-3xl shadow-sm font-sans whitespace-pre-wrap break-words [word-break:break-word] min-w-0"
    >
      <div v-if="message.content">{{ message.content }}</div>

      <!-- Attached Files Section -->
      <div
        v-if="message.attachments && message.attachments.length > 0"
        class="flex flex-col gap-1.5"
        :class="{ 'mt-2.5 pt-2 border-t border-primary-content/20': message.content }"
      >
        <div
          v-for="att in message.attachments"
          :key="att.name"
          class="flex items-center gap-2 bg-black/15 hover:bg-black/25 rounded-lg px-2.5 py-1.5 transition-colors group text-xs font-mono select-none"
        >
          <Icon :icon="getFileIcon(undefined, att.name)" class="h-4 w-4 shrink-0" />
          <span class="truncate flex-1 font-medium" :title="att.name">
            {{ att.name }}
          </span>
          <span class="text-[11px] opacity-75 shrink-0">
            {{ formatFileSize(att.size) }}
          </span>
          <a
            v-if="sessionId"
            :href="getAttachmentUrl(sessionId, att.name)"
            target="_blank"
            download
            class="btn btn-ghost btn-xs btn-circle text-primary-content opacity-75 hover:opacity-100 hover:bg-black/20 shrink-0"
            :title="$t('chat.downloadAttachment', { name: att.name })"
          >
            <Icon icon="material-symbols:download" class="h-3.5 w-3.5" />
          </a>
        </div>
      </div>
    </div>
  </div>
</template>
