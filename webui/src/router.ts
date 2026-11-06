import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import NewChatView from "./views/NewChatView.vue";
import ChatView from "./views/ChatView.vue";
import DashboardView from "./views/DashboardView.vue";
import SettingsView from "./views/SettingsView.vue";
import KeyBindingsView from "./views/KeyBindingsView.vue";
import LogView from "./views/LogView.vue";
import CompressionMetricsView from "./views/CompressionMetricsView.vue";
import UserProxyKeysView from "./views/UserProxyKeysView.vue";
import RunnersView from "./views/RunnersView.vue";

export const routes: RouteRecordRaw[] = [
  {
    path: "/",
    redirect: "/dashboard",
  },
  {
    path: "/newchat",
    name: "newchat",
    component: NewChatView,
  },
  {
    path: "/dashboard",
    name: "dashboard",
    component: DashboardView,
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
    component: SettingsView,
  },
  {
    path: "/settings/keybindings",
    name: "settings-keybindings",
    component: KeyBindingsView,
  },
  {
    path: "/settings/config",
    redirect: "/settings",
  },
  {
    path: "/settings/logs",
    name: "settings-logs",
    component: LogView,
  },
  {
    path: "/settings/compression",
    name: "settings-compression",
    component: CompressionMetricsView,
  },
  {
    path: "/settings/keys",
    name: "settings-keys",
    component: UserProxyKeysView,
  },
  {
    path: "/settings/runners",
    name: "settings-runners",
    component: RunnersView,
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});
