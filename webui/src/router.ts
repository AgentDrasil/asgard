import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";

/**
 * Router definitions for URL synchronization.
 * Views are rendered inside App.vue which watches active route parameters.
 */
const routes: RouteRecordRaw[] = [
  {
    path: "/",
    redirect: "/newchat",
  },
  {
    path: "/newchat",
    name: "newchat",
    component: { render: () => null },
  },
  {
    path: "/chat/:id",
    name: "chat",
    component: { render: () => null },
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});
