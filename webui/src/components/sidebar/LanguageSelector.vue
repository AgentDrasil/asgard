<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { SUPPORTED_LOCALES, setLocale, getLocale } from "../../i18n";

const { t } = useI18n();

const currentLocale = computed(() => getLocale());

const onLocaleChange = (event: Event) => {
  const target = event.target as HTMLSelectElement;
  setLocale(target.value, true);
};
</script>

<template>
  <div class="flex items-center">
    <select
      :value="currentLocale"
      @change="onLocaleChange"
      class="select select-bordered select-sm w-full bg-base-100 border-base-300 text-base-content focus:outline-hidden focus:ring-2 focus:ring-primary/50 text-xs font-medium cursor-pointer"
      :aria-label="t('common.language')"
    >
      <option v-for="loc in SUPPORTED_LOCALES" :key="loc.value" :value="loc.value">
        {{ loc.label }}
      </option>
    </select>
  </div>
</template>
