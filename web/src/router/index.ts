import { createRouter, createWebHistory, type RouterHistory } from "vue-router";
import { useApi } from "@/api";
import { needsSetup } from "@/features/setup/gate";
import { useAuthStore } from "@/stores/auth";
import { navigationGuard } from "./guards";
import { routes } from "./routes";

export function createAppRouter(history: RouterHistory = createWebHistory(import.meta.env.BASE_URL)) {
  const router = createRouter({
    history,
    routes,
    scrollBehavior: (_to, _from, saved) => saved ?? { top: 0 },
  });
  router.beforeEach(async (to) => {
    // A server that still needs initial setup answers everything else with
    // 503 setup_required, so the wizard is the only page worth showing.
    const setup = await needsSetup(useApi().client);
    if (setup || to.name === "setup") {
      return setup === (to.name === "setup") ? true : { name: setup ? "setup" : "login" };
    }
    const auth = useAuthStore();
    // The first navigation waits for a cookie session to be resumed, so a
    // reload keeps the user on the page they were viewing.
    await auth.restore();
    return navigationGuard(to, { isAuthenticated: auth.isAuthenticated, isAdmin: auth.isAdmin });
  });
  return router;
}
