<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { APP_THEMES } from "../../themes/terminal";

const currentTheme = ref("dark");

const daisyUiThemes = computed(() => APP_THEMES.filter((t) => t.group === "DaisyUI Themes"));
const catppuccinThemes = computed(() => APP_THEMES.filter((t) => t.group === "Catppuccin Themes"));

onMounted(() => {
  const saved = localStorage.getItem("theme");
  if (saved && APP_THEMES.some((t) => t.id === saved)) {
    currentTheme.value = saved;
  } else {
    const docTheme = document.documentElement.getAttribute("data-theme");
    if (docTheme && APP_THEMES.some((t) => t.id === docTheme)) {
      currentTheme.value = docTheme;
    }
  }
  document.documentElement.setAttribute("data-theme", currentTheme.value);
});

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
