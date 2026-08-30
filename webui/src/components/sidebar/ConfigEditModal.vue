<script setup lang="ts">
import { ref, watch } from "vue";
import { Icon } from "@iconify/vue";
import { getConfigFile, saveConfigFile } from "../../lib/api";
import { useToast } from "../../composables/useToast";

const props = defineProps<{
  modelValue: boolean;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", val: boolean): void;
  (e: "restart"): void;
}>();

const toast = useToast();

const loading = ref(false);
const saving = ref(false);
const filePath = ref("");
const configContent = ref("");
const originalContent = ref("");
const errorMessage = ref("");
const saveSuccess = ref(false);

const loadConfig = async () => {
  loading.value = true;
  errorMessage.value = "";
  saveSuccess.value = false;
  try {
    const res = await getConfigFile();
    if (res) {
      filePath.value = res.path;
      configContent.value = res.content;
      originalContent.value = res.content;
    } else {
      errorMessage.value = "Failed to load configuration file from server.";
    }
  } catch (err: any) {
    errorMessage.value = err?.message || "Failed to load configuration file.";
  } finally {
    loading.value = false;
  }
};

const handleSave = async () => {
  saving.value = true;
  errorMessage.value = "";
  saveSuccess.value = false;
  try {
    const res = await saveConfigFile(configContent.value);
    if (res.error) {
      errorMessage.value = res.error;
      toast.error(res.error, { title: "Config Save Failed" });
    } else {
      saveSuccess.value = true;
      originalContent.value = configContent.value;
      toast.success("Configuration saved, restart server to apply changes", {
        title: "Config Saved",
      });
    }
  } catch (err: any) {
    errorMessage.value = err?.message || "Failed to save configuration";
    toast.error(errorMessage.value, { title: "Config Save Failed" });
  } finally {
    saving.value = false;
  }
};

const isDirty = () => configContent.value !== originalContent.value;

const closeModal = () => {
  if (isDirty()) {
    if (
      !window.confirm("You have unsaved changes. Are you sure you want to discard them and close?")
    ) {
      return;
    }
  }
  emit("update:modelValue", false);
};

const handleModalKeydown = (e: KeyboardEvent) => {
  if (e.key === "Escape" && props.modelValue && !saving.value) {
    closeModal();
  }
};

const handleRestartClick = () => {
  emit("update:modelValue", false);
  emit("restart");
};

watch(
  () => props.modelValue,
  (isOpen) => {
    if (isOpen) {
      window.addEventListener("keydown", handleModalKeydown);
      loadConfig();
    } else {
      window.removeEventListener("keydown", handleModalKeydown);
    }
  },
  { immediate: true },
);
</script>

<template>
  <Transition name="fade">
    <div
      v-if="modelValue"
      class="fixed inset-0 bg-black/60 backdrop-blur-xs z-50 flex items-center justify-center p-4"
      @click.self="closeModal"
    >
      <div
        class="bg-base-200 border border-base-100 rounded-2xl w-full max-w-4xl max-h-[90vh] flex flex-col shadow-2xl overflow-hidden transition-all transform scale-100"
      >
        <!-- Header -->
        <div
          class="px-6 py-4 border-b border-base-100 flex items-center justify-between bg-base-300/50"
        >
          <div class="flex items-center gap-2">
            <Icon icon="mynaui:cog" class="h-6 w-6 text-primary" />
            <div>
              <h2 class="text-lg font-bold text-base-content leading-tight">Config Editor</h2>
              <span v-if="filePath" class="text-xs text-base-content/60 font-mono">{{
                filePath
              }}</span>
            </div>
          </div>
          <button
            @click="closeModal"
            class="btn btn-ghost btn-sm btn-square text-base-content/70 hover:text-base-content hover:bg-base-100/50"
          >
            <Icon icon="mynaui:x" class="h-5 w-5 fill-current" />
          </button>
        </div>

        <!-- Body -->
        <div class="p-6 overflow-y-auto flex-1 flex flex-col gap-4">
          <div v-if="loading" class="flex flex-col items-center justify-center py-16 space-y-3">
            <span class="loading loading-spinner loading-lg text-primary"></span>
            <span class="text-sm text-base-content/70">Loading configuration...</span>
          </div>

          <template v-else>
            <!-- Error Alert -->
            <div v-if="errorMessage" class="alert alert-error flex items-start gap-3 text-sm">
              <Icon icon="mynaui:danger" class="h-5 w-5 shrink-0 mt-0.5" />
              <div class="flex-1">
                <h4 class="font-bold">Validation Error</h4>
                <pre class="text-xs whitespace-pre-wrap font-mono mt-1">{{ errorMessage }}</pre>
              </div>
            </div>

            <!-- Success Notice with Restart Action -->
            <div
              v-if="saveSuccess"
              class="alert alert-success flex items-center justify-between gap-3 text-sm"
            >
              <div class="flex items-center gap-2">
                <Icon icon="mynaui:check-circle" class="h-5 w-5 shrink-0" />
                <span>Configuration saved, restart server to apply changes</span>
              </div>
              <button
                @click="handleRestartClick"
                class="btn btn-sm btn-outline border-success-content/30 text-success-content hover:bg-success-content/20 gap-1.5"
              >
                <Icon icon="mynaui:refresh" class="h-4 w-4" />
                <span>Restart Server</span>
              </button>
            </div>

            <!-- Code Editor Area -->
            <div class="flex-1 flex flex-col min-h-[350px]">
              <textarea
                v-model="configContent"
                class="textarea textarea-bordered font-mono text-xs md:text-sm w-full flex-1 resize-none bg-base-300/60 p-4 leading-relaxed focus:outline-hidden focus:border-primary"
                placeholder="YAML configuration..."
                spellcheck="false"
              ></textarea>
            </div>
          </template>
        </div>

        <!-- Footer -->
        <div
          class="px-6 py-4 border-t border-base-100 flex items-center justify-between bg-base-300/30"
        >
          <div class="flex items-center gap-2">
            <button
              @click="loadConfig"
              class="btn btn-outline btn-sm gap-2"
              :disabled="loading || saving"
              title="Reload configuration from disk"
            >
              <Icon
                icon="mynaui:refresh"
                :class="['h-4 w-4 fill-current', { 'animate-spin': loading }]"
              />
              <span>Reload</span>
            </button>
          </div>

          <div class="flex items-center gap-2">
            <button @click="closeModal" class="btn btn-ghost btn-sm" :disabled="saving">
              Cancel
            </button>
            <button
              @click="handleSave"
              class="btn btn-primary btn-sm gap-2"
              :disabled="loading || saving"
            >
              <span v-if="saving" class="loading loading-spinner loading-xs"></span>
              <Icon v-else icon="mynaui:save" class="h-4 w-4 fill-current" />
              <span>Save Configuration</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  </Transition>
</template>
