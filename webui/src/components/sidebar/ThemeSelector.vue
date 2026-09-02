<script setup lang="ts">
import { ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import { APP_THEMES } from "../../themes/terminal";

const { t } = useI18n();

const currentTheme = ref(document.documentElement.getAttribute("data-theme") || "dark");

const daisyUiThemes = computed(() => APP_THEMES.filter((t) => t.group === "DaisyUI Themes"));
const catppuccinThemes = computed(() => APP_THEMES.filter((t) => t.group === "Catppuccin Themes"));

const onThemeChange = (event: Event) => {
  const target = event.target as HTMLSelectElement;
  const themeId = target.value;
  currentTheme.value = themeId;
  document.documentElement.setAttribute("data-theme", themeId);
  localStorage.setItem("theme", themeId);
};
</script>

<template>
  <div class="flex items-center">
    <select
      :value="currentTheme"
      @change="onThemeChange"
      class="select select-bordered select-sm w-48 sm:w-56 bg-base-100 border-base-300 text-base-content focus:outline-hidden focus:ring-2 focus:ring-primary/50 text-xs font-medium cursor-pointer"
      :aria-label="t('theme.selectTheme')"
    >
      <optgroup :label="t('theme.daisyUiThemes')">
        <option v-for="th in daisyUiThemes" :key="th.id" :value="th.id">
          {{ th.name }}
        </option>
      </optgroup>
      <optgroup :label="t('theme.catppuccinThemes')">
        <option v-for="th in catppuccinThemes" :key="th.id" :value="th.id">
          {{ th.name }}
        </option>
      </optgroup>
    </select>
  </div>
</template>
