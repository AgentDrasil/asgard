import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import NewChatView from "./views/NewChatView.vue";
import ChatView from "./views/ChatView.vue";
import DashboardView from "./views/DashboardView.vue";
import SettingsView from "./views/SettingsView.vue";
import KeyBindingsView from "./views/KeyBindingsView.vue";
import ConfigEditView from "./views/ConfigEditView.vue";
import LogView from "./views/LogView.vue";

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
    name: "settings-config",
    component: ConfigEditView,
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
