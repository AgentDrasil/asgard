import { createApp } from "vue";
import App from "./App.vue";
import { router } from "./router";
import { i18n, initI18nWithBackend } from "./i18n";
import { getBackendConfig, getStoredTokens, initAuth } from "./lib/api";
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

// loadBackendConfig applies the backend-provided defaults (e.g. UI language)
// after the app is mounted.
const loadBackendConfig = () => {
  getBackendConfig()
    .then((cfg) => {
      if (cfg.default_ui_lang) {
        initI18nWithBackend(cfg.default_ui_lang);
      }
    })
    .catch((err) => {
      console.warn("Failed to fetch backend config for i18n default:", err);
    });
};

// The capability probe must complete before mounting so the very first request
// already knows whether to send a bearer token, and so an unauthenticated
// visitor is sent to the provider before the SPA renders and fires doomed API
// calls.
const bootstrap = async () => {
  const authed = await initAuth();
  if (authed && !getStoredTokens()) {
    const current = new URL(window.location.href);
    const redirect = encodeURIComponent(current.pathname + current.search);
    window.location.replace(`/auth/login?redirect=${redirect}`);
    return;
  }
  app.mount("#app");
  loadBackendConfig();
};

void bootstrap();
