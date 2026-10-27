import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    chunkSizeWarningLimit: 3000,
  },
  server: {
    port: 8082,
    host: "127.0.0.1",
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
      "/agents": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  test: {
    // Node >= 22.4 exposes an experimental global `localStorage` getter that
    // emits an ExperimentalWarning once per worker when the jsdom environment
    // initializes. Silence just that warning class in test workers.
    execArgv: ["--disable-warning=ExperimentalWarning"],
    setupFiles: ["./src/test/setup.ts"],
  },
});
