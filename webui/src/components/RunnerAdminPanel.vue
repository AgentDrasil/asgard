<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { getManageRunners, setRunnerUsers } from "../lib/api";
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

const userLabel = (u: ManageUser) => u.email || u.name || u.subject;

async function load() {
  loading.value = true;
  const res = await getManageRunners();
  if (res) {
    runners.value = res.runners;
    users.value = res.users;
  }
  loading.value = false;
}

async function toggle(r: ManageRunner, subject: string, checked: boolean) {
  const next = checked ? [...r.users, subject] : r.users.filter((s) => s !== subject);
  saving.value = r.node_id;
  const err = await setRunnerUsers(r.node_id, next);
  saving.value = "";
  if (err) {
    toast.error(`${t("runnerAdmin.saveFailed")}: ${err}`);
  } else {
    r.users = next;
    void refreshRunners();
  }
}

onMounted(() => void load());
</script>

<template>
  <section class="space-y-3" data-testid="runner-admin-panel">
    <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
      {{ t("runnerAdmin.title") }}
    </h2>
    <div class="rounded-xl border border-base-300 bg-base-200/50 p-4 md:p-5 space-y-3">
      <p class="text-xs text-base-content/70">{{ t("runnerAdmin.desc") }}</p>
      <p v-if="!loading && runners.length === 0" class="text-sm text-base-content/60">
        {{ t("runners.noRunners") }}
      </p>
      <p v-else-if="!loading && users.length === 0" class="text-sm text-base-content/60">
        {{ t("runnerAdmin.noUsers") }}
      </p>
      <div
        v-for="r in runners"
        :key="r.node_id"
        class="py-2 space-y-1"
        data-testid="runner-admin-row"
      >
        <div class="font-mono font-medium">{{ r.node_id }}</div>
        <p v-if="r.users.length === 0" class="text-xs text-warning">
          {{ t("runnerAdmin.unassigned") }}
        </p>
        <div class="flex flex-wrap gap-x-4 gap-y-1">
          <label v-for="u in users" :key="u.subject" class="flex items-center gap-1.5 text-sm">
            <input
              type="checkbox"
              class="checkbox checkbox-xs"
              :checked="r.users.includes(u.subject)"
              :disabled="saving === r.node_id"
              @change="toggle(r, u.subject, ($event.target as HTMLInputElement).checked)"
            />
            <span>{{ userLabel(u) }}</span>
          </label>
        </div>
      </div>
    </div>
  </section>
</template>
