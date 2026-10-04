// Composition root for Vue app plugins: API context, Pinia, i18n and router,
// then the extension host (G32) and administrator CSS (G33.4). Extensions
// start after these core plugins, so they never run before session handling
// exists.
import { createPinia } from "pinia";
import type { App } from "vue";
import type { RouterHistory } from "vue-router";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import { createAppI18n } from "@/i18n";
import { negotiateLocale } from "@/i18n/locales";
import { createAppRouter } from "@/router";
import { useAuthStore } from "@/stores/auth";
import type { PluginHostOptions } from "./host/store";

export interface AppPluginOptions {
  languages?: readonly string[];
  fetch?: typeof globalThis.fetch;
  history?: RouterHistory;
  /** Replaces the bundled plugins (tests). */
  plugins?: PluginHostOptions;
}

export function installAppPlugins(app: App, options: AppPluginOptions = {}) {
  const locale = negotiateLocale(options.languages ?? navigator.languages);
  document.documentElement.lang = locale;
  const i18n = createAppI18n(locale);
  const pinia = createPinia();
  let handleUnauthorized = () => {};
  const api = createApiClient({
    ...(options.fetch ? { fetch: options.fetch } : {}),
    locale: () => i18n.global.locale.value,
    onUnauthorized: () => {
      handleUnauthorized();
    },
  });
  app.provide(apiKey, api);
  app.use(pinia);
  app.use(i18n);
  const router = createAppRouter(options.history);
  app.use(router);
  // The extension host (manifest validation, enabled plugins, their routes
  // and theme tokens) and administrator CSS start from a separate chunk.
  const extensions = import("./host/store").then(async ({ pluginHostKey }) => {
    if (options.plugins) {
      app.provide(pluginHostKey, options.plugins);
    }
    const { startExtensions } = await import("./host/start");
    return startExtensions(app);
  });
  // A deep link to a plugin page resolves to 404 until plugins have loaded
  // and registered their routes; retry it once they have.
  router.beforeEach(async (to) => {
    if (to.name !== "not-found" || !to.path.startsWith("/x/")) {
      return true;
    }
    const plugins = await extensions;
    await plugins.settled();
    return router.resolve(to.fullPath).name === "not-found" ? true : to.fullPath;
  });
  handleUnauthorized = () => {
    app.runWithContext(() => useAuthStore()).expire();
    const current = router.currentRoute.value;
    if (current.meta.public !== true) {
      void router.replace({ name: "login", query: { reason: "expired", redirect: current.fullPath } });
    }
  };
  return { router, i18n, pinia, api, extensions };
}
