import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [vue(), tailwindcss()],
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
});
