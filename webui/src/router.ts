import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import NewChatView from "./views/NewChatView.vue";
import ChatView from "./views/ChatView.vue";

const routes: RouteRecordRaw[] = [
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
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});
