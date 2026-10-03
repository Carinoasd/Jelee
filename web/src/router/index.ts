import { createRouter, createWebHistory, type RouterHistory } from "vue-router";
import { useAuthStore } from "@/stores/auth";
import { navigationGuard } from "./guards";
import { routes } from "./routes";

export function createAppRouter(history: RouterHistory = createWebHistory(import.meta.env.BASE_URL)) {
  const router = createRouter({ history, routes });
  router.beforeEach((to) => {
    const auth = useAuthStore();
    return navigationGuard(to, { isAuthenticated: auth.isAuthenticated, isAdmin: auth.user?.admin === true });
  });
  return router;
}
