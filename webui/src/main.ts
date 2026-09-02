import { createApp } from "vue";
import App from "./App.vue";
import { router } from "./router";
import { i18n, initI18nWithBackend } from "./i18n";
import { getBackendConfig } from "./lib/api";
import { APP_THEMES } from "./themes/terminal";
import "katex/dist/katex.min.css";
import "./index.css";

const isValidTheme = (id: string | null): id is string =>
  id !== null && APP_THEMES.some((t) => t.id === id);

const saved = localStorage.getItem("theme");
const docTheme = document.documentElement.getAttribute("data-theme");
const theme = isValidTheme(saved) ? saved : isValidTheme(docTheme) ? docTheme : "dark";
document.documentElement.setAttribute("data-theme", theme);

const app = createApp(App);
app.use(i18n);
app.use(router);
app.mount("#app");

getBackendConfig()
  .then((cfg) => {
    if (cfg.default_ui_lang) {
      initI18nWithBackend(cfg.default_ui_lang);
    }
  })
  .catch((err) => {
    console.warn("Failed to fetch backend config for i18n default:", err);
  });
