import { h } from "vue";
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import NewChatView from "./views/NewChatView.vue";
import ChatView from "./views/ChatView.vue";
import LogView from "./views/LogView.vue";

export const SettingsPlaceholder = () =>
  h("div", { class: "p-6 text-base-content/70" }, "Settings");

export const ConfigEditPlaceholder = () =>
  h("div", { class: "p-6 text-base-content/70" }, "Config Editor");

export const routes: RouteRecordRaw[] = [
  {
    path: "/",
    redirect: "/newchat",
  },
  {
    path: "/newchat",
    name: "newchat",
    component: NewChatView,
  },
  {
    path: "/chat/:id",
    name: "chat",
    component: ChatView,
  },
  {
    path: "/chat/:id/files/:filePath(.*)*",
    name: "chat-files",
    component: ChatView,
  },
  {
    path: "/chat/:id/vcs/:commitId?/:filePath(.*)*",
    name: "chat-vcs",
    component: ChatView,
  },
  {
    path: "/settings",
    name: "settings",
    component: SettingsPlaceholder,
  },
  {
    path: "/settings/config",
    name: "settings-config",
    component: ConfigEditPlaceholder,
  },
  {
    path: "/settings/logs",
    name: "settings-logs",
    component: LogView,
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});
