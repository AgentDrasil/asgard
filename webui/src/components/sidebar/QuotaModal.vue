<script setup lang="ts">
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { apiFetch } from "../../lib/api";
import { formatQuotaResetRelative } from "../../i18n/timeUtils";

const { t } = useI18n();

interface QuotaLimit {
  name: string;
  remaining: number;
  refresh_date?: number;
}

interface ModelUsage {
  model: string;
  remaining: number;
  refresh_date?: number;
  limits?: QuotaLimit[];
}

const props = defineProps<{
  modelValue: boolean;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", val: boolean): void;
}>();

const quotaLoading = ref(false);
const quotaError = ref("");
const quotas = ref<Record<string, ModelUsage[]>>({});

const fetchQuotas = async () => {
  quotaLoading.value = true;
  quotaError.value = "";
  try {
    const res = await apiFetch("/api/quota");
    if (!res.ok) {
      throw new Error(t("quota.statusError", { status: res.status }));
    }
    const data = await res.json();
    quotas.value = data;
  } catch (err: any) {
    console.error("Failed to fetch quotas:", err);
    quotaError.value = err.message || t("quota.failedToFetch");
  } finally {
    quotaLoading.value = false;
  }
};

const closeModal = () => {
  emit("update:modelValue", false);
};

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      fetchQuotas();
    }
  },
  { immediate: true },
);

const getProgressClass = (fraction: number) => {
  if (fraction <= 0.2) return "progress-error";
  if (fraction <= 0.5) return "progress-warning";
  return "progress-success";
};

const getTextColorClass = (fraction: number) => {
  if (fraction <= 0.2) return "text-error";
  if (fraction <= 0.5) return "text-warning";
  return "text-success";
};

const formatRefreshDate = (timestamp?: number) => {
  if (!timestamp) return t("time.noResetPending");
  const date = new Date(timestamp * 1000);
  return date.toLocaleString();
};

defineExpose({
  fetchQuotas,
});
</script>

<template>
  <Transition name="fade">
    <div
      v-if="modelValue"
      class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-center justify-center p-4"
      @click.self="closeModal"
    >
      <div
        class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-xl max-h-[85vh] flex flex-col shadow-2xl overflow-hidden transition-all transform scale-100"
      >
        <!-- Header -->
        <div
          class="px-6 py-4 border-b border-base-100 flex items-center justify-between bg-base-300/50"
        >
          <div class="flex items-center gap-2">
            <Icon icon="mynaui:chart-bar-one" class="h-6 w-6 text-primary" />
            <h2 class="text-lg font-bold text-base-content">{{ t("quota.title") }}</h2>
          </div>
          <button
            @click="closeModal"
            class="btn btn-ghost btn-sm btn-square text-base-content/70 hover:text-base-content hover:bg-base-100/50"
          >
            <Icon icon="mynaui:x" class="h-5 w-5 fill-current" />
          </button>
        </div>

        <!-- Body -->
        <div class="p-6 overflow-y-auto flex-1 space-y-6">
          <div
            v-if="quotaLoading"
            class="flex flex-col items-center justify-center py-12 space-y-3"
          >
            <span class="loading loading-spinner loading-lg text-primary"></span>
            <span class="text-sm text-base-content/70">{{ t("quota.fetching") }}</span>
          </div>

          <div v-else-if="quotaError" class="alert alert-error flex items-start gap-3">
            <Icon icon="mynaui:danger" class="h-6 w-6 shrink-0" />
            <div>
              <h3 class="font-bold">{{ t("quota.errorTitle") }}</h3>
              <div class="text-xs">{{ quotaError }}</div>
            </div>
          </div>

          <div v-else class="space-y-6">
            <div v-for="(models, cliName) in quotas" :key="cliName" class="space-y-3">
              <div class="flex items-center gap-2 border-b border-base-100/60 pb-1.5">
                <span class="text-xs font-bold uppercase tracking-wider text-primary/80">{{
                  t("quota.cli")
                }}</span>
                <span
                  class="text-sm font-semibold capitalize bg-primary/10 text-primary px-2.5 py-0.5 rounded-full"
                  >{{ cliName }}</span
                >
              </div>

              <div class="space-y-4">
                <div
                  v-for="m in models"
                  :key="m.model"
                  class="bg-base-300/40 border border-base-100/30 rounded-xl p-4 space-y-3"
                >
                  <div class="flex justify-between items-start">
                    <h4 class="font-medium text-sm text-base-content">{{ m.model }}</h4>
                    <span
                      class="text-xs font-semibold px-2 py-0.5 rounded-md"
                      :class="[
                        m.remaining <= 0.2
                          ? 'bg-error/10 text-error'
                          : m.remaining <= 0.5
                            ? 'bg-warning/10 text-warning'
                            : 'bg-success/10 text-success',
                      ]"
                    >
                      {{ t("quota.remaining", { pct: Math.round(m.remaining * 100) }) }}
                    </span>
                  </div>

                  <!-- Single Progress Bar (when no multi-tier breakdown limits exist) -->
                  <div v-if="!m.limits || m.limits.length === 0" class="space-y-1">
                    <progress
                      class="progress w-full"
                      :class="getProgressClass(m.remaining)"
                      :value="m.remaining * 100"
                      max="100"
                    ></progress>
                    <div class="flex justify-between text-[11px] text-base-content/50">
                      <span>0%</span>
                      <span v-if="m.refresh_date" class="italic text-right truncate max-w-[80%]">
                        {{
                          t("quota.resetsAt", {
                            date: formatRefreshDate(m.refresh_date),
                            relative: formatQuotaResetRelative(m.refresh_date, t),
                          })
                        }}
                      </span>
                      <span>100%</span>
                    </div>
                  </div>

                  <!-- Specific Detailed Limits (if any) -->
                  <div v-else class="space-y-2">
                    <h5 class="text-[11px] font-bold uppercase tracking-wider text-base-content/40">
                      {{ t("quota.limitsBreakdown") }}
                    </h5>
                    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                      <div
                        v-for="lim in m.limits"
                        :key="lim.name"
                        class="bg-base-200/50 border border-base-100/20 rounded-lg p-2.5 space-y-1.5"
                      >
                        <div class="flex justify-between items-center">
                          <span class="text-xs font-semibold text-base-content/80 capitalize">{{
                            lim.name
                          }}</span>
                          <span
                            class="text-[11px] font-medium"
                            :class="getTextColorClass(lim.remaining)"
                          >
                            {{ Math.round(lim.remaining * 100) }}%
                          </span>
                        </div>
                        <progress
                          class="progress progress-xs w-full"
                          :class="getProgressClass(lim.remaining)"
                          :value="lim.remaining * 100"
                          max="100"
                        ></progress>
                        <div
                          v-if="lim.refresh_date"
                          class="text-[9px] text-base-content/40 truncate"
                        >
                          {{
                            t("quota.resetPrefix", {
                              date: formatRefreshDate(lim.refresh_date),
                            })
                          }}
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div
              v-if="Object.keys(quotas).length === 0"
              class="text-center py-8 text-base-content/50 text-sm"
            >
              {{ t("quota.noQuotaInfo") }}
            </div>
          </div>
        </div>

        <!-- Footer -->
        <div class="px-6 py-4 border-t border-base-100 flex justify-between bg-base-300/30">
          <button
            @click="fetchQuotas"
            class="btn btn-outline btn-sm gap-2"
            :disabled="quotaLoading"
          >
            <Icon
              icon="mynaui:refresh"
              :class="['h-4 w-4 fill-current', { 'animate-spin': quotaLoading }]"
            />
            {{ t("quota.refresh") }}
          </button>
          <button @click="closeModal" class="btn btn-primary btn-sm">{{ t("quota.close") }}</button>
        </div>
      </div>
    </div>
  </Transition>
</template>
