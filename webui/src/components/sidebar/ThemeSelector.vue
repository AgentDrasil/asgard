<script setup lang="ts">
import { ref, computed } from "vue";
import { APP_THEMES } from "../../themes/terminal";

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
      aria-label="Select Theme"
    >
      <optgroup label="DaisyUI Themes">
        <option v-for="t in daisyUiThemes" :key="t.id" :value="t.id">
          {{ t.name }}
        </option>
      </optgroup>
      <optgroup label="Catppuccin Themes">
        <option v-for="t in catppuccinThemes" :key="t.id" :value="t.id">
          {{ t.name }}
        </option>
      </optgroup>
    </select>
  </div>
</template>
