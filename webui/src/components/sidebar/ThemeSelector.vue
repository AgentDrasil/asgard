<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { Icon } from "@iconify/vue";
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

const selectTheme = (themeId: string) => {
  currentTheme.value = themeId;
  document.documentElement.setAttribute("data-theme", themeId);
  localStorage.setItem("theme", themeId);

  if (document.activeElement instanceof HTMLElement) {
    document.activeElement.blur();
  }
};
</script>

<template>
  <div class="dropdown dropdown-top">
    <button
      tabindex="0"
      role="button"
      class="btn btn-ghost btn-xs btn-circle text-base-content/70 hover:text-base-content"
      title="Select Theme"
    >
      <Icon icon="mdi:paint-outline" class="h-5 w-5 fill-current" />
    </button>
    <ul
      tabindex="0"
      class="dropdown-content menu menu-sm bg-base-200 border border-base-100 rounded-box z-50 w-52 p-1.5 shadow-xl max-h-60 overflow-y-auto mb-1"
    >
      <li class="menu-title text-[10px] uppercase font-semibold text-base-content/50 px-2 py-1">
        DaisyUI Themes
      </li>
      <li v-for="t in daisyUiThemes" :key="t.id">
        <button
          @click="selectTheme(t.id)"
          :class="[
            'flex items-center justify-between py-1 px-2 text-xs rounded-md',
            currentTheme === t.id ? 'active font-medium' : '',
          ]"
        >
          <span>{{ t.name }}</span>
          <Icon v-if="currentTheme === t.id" icon="mynaui:check" class="w-4 h-4 shrink-0" />
        </button>
      </li>
      <li
        class="menu-title text-[10px] uppercase font-semibold text-base-content/50 px-2 py-1 mt-1"
      >
        Catppuccin Themes
      </li>
      <li v-for="t in catppuccinThemes" :key="t.id">
        <button
          @click="selectTheme(t.id)"
          :class="[
            'flex items-center justify-between py-1 px-2 text-xs rounded-md',
            currentTheme === t.id ? 'active font-medium' : '',
          ]"
        >
          <span>{{ t.name }}</span>
          <Icon v-if="currentTheme === t.id" icon="mynaui:check" class="w-4 h-4 shrink-0" />
        </button>
      </li>
    </ul>
  </div>
</template>
