<script setup lang="ts">
import { onMounted, onUnmounted, ref, computed, useId, nextTick } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { getManageRunners, setRunnerUsers, appendAccessToken } from "../lib/api";
import { useRunners } from "../composables/useRunners";
import { useToast } from "../composables/useToast";
import type { ManageRunner, ManageUser } from "../types";

const { t } = useI18n();
const toast = useToast();
const { refreshRunners } = useRunners();

const runners = ref<ManageRunner[]>([]);
const users = ref<ManageUser[]>([]);
const loading = ref(false);
const saving = ref("");
const searchQueries = ref<Record<string, string>>({});
const baseId = useId();
const failedAvatars = ref<Record<string, boolean>>({});

const usersMap = computed(() => new Map(users.value.map((u) => [u.subject, u])));

function getUserInitials(u?: ManageUser): string {
  if (!u) return "?";
  const source = u.name || u.email || u.subject || "";
  const parts = source.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  return parts
    .slice(0, 2)
    .map((p) => p[0].toUpperCase())
    .join("");
}

function userDisplayName(u?: ManageUser): string {
  if (!u) return "";
  return u.name || u.email || u.subject;
}

function availableUsers(r: ManageRunner): ManageUser[] {
  return users.value.filter((u) => !r.users.includes(u.subject));
}

function filteredUsers(r: ManageRunner): ManageUser[] {
  const q = (searchQueries.value[r.node_id] || "").trim().toLowerCase();
  const available = availableUsers(r);
  if (!q) return available.slice(0, 10);
  return available.filter((u) => {
    const name = (u.name || "").toLowerCase();
    const email = (u.email || "").toLowerCase();
    const sub = u.subject.toLowerCase();
    return name.includes(q) || email.includes(q) || sub.includes(q);
  });
}

async function load() {
  loading.value = true;
  try {
    const res = await getManageRunners();
    if (res) {
      runners.value = res.runners;
      users.value = res.users;
    }
  } catch (err) {
    toast.error(String(err));
  } finally {
    loading.value = false;
  }
}

async function addUser(r: ManageRunner, subject: string) {
  if (r.users.includes(subject)) return;
  const next = [...r.users, subject];
  saving.value = r.node_id;
  const err = await setRunnerUsers(r.node_id, next);
  saving.value = "";
  if (err) {
    toast.error(`${t("runnerAdmin.saveFailed")}: ${err}`);
  } else {
    r.users = next;
    searchQueries.value[r.node_id] = "";
    closePopover(r.node_id);
    toast.success(t("runnerAdmin.userAssignedSuccess"));
    void refreshRunners();
  }
}

async function removeUser(r: ManageRunner, subject: string) {
  const next = r.users.filter((s) => s !== subject);
  saving.value = r.node_id;
  const err = await setRunnerUsers(r.node_id, next);
  saving.value = "";
  if (err) {
    toast.error(`${t("runnerAdmin.saveFailed")}: ${err}`);
  } else {
    r.users = next;
    toast.success(t("runnerAdmin.userRemovedSuccess"));
    void refreshRunners();
  }
}

function popoverId(i: number): string {
  return `${baseId}-users-${i}`;
}

const popoverEls = new Map<string, HTMLElement>();

function setPopoverEl(nodeId: string, el: unknown) {
  if (el instanceof HTMLElement) popoverEls.set(nodeId, el);
  else popoverEls.delete(nodeId);
}

function closePopover(nodeId: string) {
  const el = popoverEls.get(nodeId);
  if (el?.matches(":popover-open")) el.hidePopover();
}

// Popovers render in the top layer, so position them against the trigger.
function placePopoverEl(el: HTMLElement) {
  const trigger = el.parentElement?.querySelector<HTMLElement>("[popovertarget]");
  if (!trigger) return;
  const margin = 8;
  const rect = trigger.getBoundingClientRect();
  const width = Math.min(448, window.innerWidth - margin * 2);
  el.style.width = `${width}px`;
  const height = el.offsetHeight;
  let top = rect.bottom + 4;
  if (height > 0 && top + height > window.innerHeight - margin) {
    const above = rect.top - 4 - height;
    top = above >= margin ? above : Math.max(margin, window.innerHeight - margin - height);
  }
  el.style.top = `${top}px`;
  el.style.left = `${Math.max(margin, Math.min(rect.left, window.innerWidth - width - margin))}px`;
}

function placePopover(e: Event) {
  const ev = e as ToggleEvent;
  if (ev.newState !== "open") return;
  placePopoverEl(ev.currentTarget as HTMLElement);
}

function repositionOpenPopovers() {
  for (const el of popoverEls.values()) {
    if (el.matches(":popover-open")) placePopoverEl(el);
  }
}

function onPopoverToggle(e: Event) {
  const ev = e as ToggleEvent;
  const el = ev.currentTarget as HTMLElement;
  if (ev.newState === "open") {
    // Now rendered: height is known, so flip/clamp precisely.
    placePopoverEl(el);
    void nextTick(() => el.querySelector<HTMLInputElement>("input")?.focus());
  }
}

onMounted(() => {
  void load();
  window.addEventListener("resize", repositionOpenPopovers);
  document.addEventListener("scroll", repositionOpenPopovers, true);
});

onUnmounted(() => {
  window.removeEventListener("resize", repositionOpenPopovers);
  document.removeEventListener("scroll", repositionOpenPopovers, true);
});
</script>

<template>
  <section class="space-y-3" data-testid="runner-admin-panel">
    <div class="flex items-center justify-between gap-2">
      <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
        {{ t("runnerAdmin.title") }}
      </h2>
      <button
        class="btn btn-ghost btn-xs gap-1.5"
        :disabled="loading"
        data-testid="runner-admin-refresh"
        @click="load"
      >
        <Icon icon="mynaui:refresh" :class="['h-4 w-4', { 'animate-spin': loading }]" />
        <span>{{ t("runners.refresh") }}</span>
      </button>
    </div>

    <div class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 space-y-4">
      <p class="text-xs text-base-content/70">{{ t("runnerAdmin.desc") }}</p>

      <div v-if="loading" class="flex justify-center py-6">
        <span class="loading loading-spinner loading-sm text-primary"></span>
      </div>

      <p v-else-if="runners.length === 0" class="text-sm text-base-content/60">
        {{ t("runners.noRunners") }}
      </p>

      <p v-else-if="users.length === 0" class="text-sm text-base-content/60">
        {{ t("runnerAdmin.noUsers") }}
      </p>

      <div v-else class="space-y-4 divide-y divide-base-300">
        <div
          v-for="(r, ri) in runners"
          :key="r.node_id"
          class="pt-3 first:pt-0 space-y-3"
          data-testid="runner-admin-row"
        >
          <!-- Runner Header -->
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <span
                :class="[
                  'inline-block h-2.5 w-2.5 rounded-full shrink-0',
                  r.online ? 'bg-success' : 'bg-error',
                ]"
              />
              <span class="font-mono font-medium text-sm text-base-content">{{ r.node_id }}</span>
              <span :class="['badge badge-xs', r.online ? 'badge-success' : 'badge-error']">
                {{ r.online ? t("runners.online") : t("runners.offline") }}
              </span>
            </div>
            <span class="text-xs text-base-content/60">
              {{ t("runnerAdmin.assignedUsers") }}: {{ r.users.length }}
            </span>
          </div>

          <!-- Unassigned Warning -->
          <p v-if="r.users.length === 0" class="text-xs text-warning flex items-center gap-1.5">
            <Icon icon="mynaui:danger" class="h-4 w-4 shrink-0" />
            <span>{{ t("runnerAdmin.unassigned") }}</span>
          </p>

          <!-- Assigned Users Chips -->
          <div
            v-if="r.users.length > 0"
            class="flex flex-wrap gap-2"
            data-testid="assigned-users-list"
          >
            <div
              v-for="sub in r.users"
              :key="sub"
              class="inline-flex items-center gap-2 rounded-lg border border-base-300 bg-base-100 px-2.5 py-1 text-xs shadow-xs"
              data-testid="assigned-user-chip"
            >
              <img
                v-if="usersMap.get(sub)?.avatarUrl && !failedAvatars[sub]"
                :src="appendAccessToken(usersMap.get(sub)!.avatarUrl!)"
                alt=""
                class="w-5 h-5 rounded-full object-cover shrink-0"
                @error="failedAvatars[sub] = true"
              />
              <span
                v-else
                class="w-5 h-5 rounded-full bg-primary/20 text-primary text-[10px] font-bold flex items-center justify-center shrink-0"
              >
                {{ getUserInitials(usersMap.get(sub)) }}
              </span>

              <div class="flex items-center gap-1 min-w-0">
                <span class="font-medium text-base-content truncate max-w-[120px]">
                  {{ userDisplayName(usersMap.get(sub)) || sub }}
                </span>
                <span
                  v-if="usersMap.get(sub)?.email && usersMap.get(sub)?.name"
                  class="text-[11px] text-base-content/50 truncate max-w-[120px]"
                >
                  ({{ usersMap.get(sub)!.email }})
                </span>
              </div>

              <button
                type="button"
                class="btn btn-ghost btn-xs btn-circle text-base-content/50 hover:text-error shrink-0"
                :disabled="saving === r.node_id"
                :title="t('runnerAdmin.removeUser')"
                :aria-label="t('runnerAdmin.removeUser')"
                data-testid="remove-user-btn"
                @click="removeUser(r, sub)"
              >
                <Icon icon="mynaui:x" class="w-3.5 h-3.5" />
              </button>
            </div>
          </div>

          <!-- Add User Search Box -->
          <div class="relative w-full max-w-md pt-1">
            <button
              type="button"
              class="btn btn-outline btn-sm gap-1 text-xs"
              :popovertarget="popoverId(ri)"
              aria-haspopup="listbox"
              data-testid="add-user-btn"
            >
              <Icon icon="mynaui:plus" class="w-3.5 h-3.5" />
              <span>{{ t("runnerAdmin.addUser") }}</span>
            </button>

            <!-- Search Dropdown Results (native popover: light dismiss, Esc) -->
            <div
              :id="popoverId(ri)"
              popover="auto"
              :ref="(el) => setPopoverEl(r.node_id, el)"
              class="m-0 inset-auto fixed max-h-72 overflow-y-auto rounded-xl border border-base-300 bg-base-100 p-1 text-base-content shadow-xl"
              data-testid="user-search-dropdown"
              @beforetoggle="placePopover"
              @toggle="onPopoverToggle"
            >
              <div
                class="flex items-center gap-2 input input-sm input-bordered bg-base-100 rounded-lg w-full shadow-xs"
              >
                <Icon icon="mynaui:search" class="w-4 h-4 text-base-content/40 shrink-0" />
                <input
                  v-model="searchQueries[r.node_id]"
                  type="text"
                  class="w-full bg-transparent focus:outline-hidden text-xs"
                  :placeholder="t('runnerAdmin.searchUsersPlaceholder')"
                  :aria-label="t('runnerAdmin.searchUsersPlaceholder')"
                  :aria-controls="
                    availableUsers(r).length > 0 && filteredUsers(r).length > 0
                      ? `${popoverId(ri)}-list`
                      : undefined
                  "
                  data-testid="search-users-input"
                />
                <button
                  v-if="searchQueries[r.node_id]"
                  type="button"
                  class="btn btn-ghost btn-xs btn-circle text-base-content/50"
                  :aria-label="t('runnerAdmin.clearSearch')"
                  @click="searchQueries[r.node_id] = ''"
                >
                  <Icon icon="mynaui:x" class="w-3.5 h-3.5" />
                </button>
              </div>
              <div
                v-if="availableUsers(r).length === 0"
                class="py-3 px-3 text-center text-xs text-base-content/50"
              >
                {{ t("runnerAdmin.allUsersAssigned") }}
              </div>
              <div
                v-else-if="filteredUsers(r).length === 0"
                class="py-3 px-3 text-center text-xs text-base-content/50"
              >
                {{ t("runnerAdmin.noMatchingUsers") }}
              </div>
              <ul
                v-else
                :id="`${popoverId(ri)}-list`"
                role="listbox"
                :aria-label="t('runnerAdmin.availableUsers')"
                class="menu menu-xs mt-1 p-0 w-full"
              >
                <li v-for="u in filteredUsers(r)" :key="u.subject" role="presentation">
                  <button
                    type="button"
                    role="option"
                    :aria-selected="false"
                    class="flex items-center justify-between gap-3 py-2 px-2.5 rounded-lg hover:bg-base-200"
                    :disabled="saving === r.node_id"
                    data-testid="user-option"
                    @click="addUser(r, u.subject)"
                  >
                    <div class="flex items-center gap-2.5 min-w-0">
                      <img
                        v-if="u.avatarUrl && !failedAvatars[u.subject]"
                        :src="appendAccessToken(u.avatarUrl)"
                        alt=""
                        class="w-6 h-6 rounded-full object-cover shrink-0"
                        @error="failedAvatars[u.subject] = true"
                      />
                      <span
                        v-else
                        class="w-6 h-6 rounded-full bg-primary/20 text-primary text-xs font-bold flex items-center justify-center shrink-0"
                      >
                        {{ getUserInitials(u) }}
                      </span>
                      <div class="flex flex-col text-left min-w-0">
                        <span class="font-medium text-xs text-base-content truncate">
                          {{ userDisplayName(u) }}
                        </span>
                        <span v-if="u.email" class="text-[11px] text-base-content/60 truncate">
                          {{ u.email }}
                        </span>
                        <span
                          v-else-if="u.name"
                          class="text-[10px] text-base-content/40 font-mono truncate"
                        >
                          {{ u.subject }}
                        </span>
                      </div>
                    </div>
                    <span class="btn btn-primary btn-xs shrink-0">
                      <Icon icon="mynaui:plus" class="w-3.5 h-3.5" />
                      <span>{{ t("runnerAdmin.addUser") }}</span>
                    </span>
                  </button>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
