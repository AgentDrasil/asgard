import { createRouter, createWebHistory } from "vue-router";

const routes = [
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
