// Composition root for Vue app plugins: API context, Pinia, i18n and router.
// The extension SDK host (G32) will register here as well, after these core
// plugins, so extensions can never run before session handling exists.
import { createPinia } from "pinia";
import type { App } from "vue";
import type { RouterHistory } from "vue-router";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import { createAppI18n } from "@/i18n";
import { negotiateLocale } from "@/i18n/locales";
import { createAppRouter } from "@/router";
import { useAuthStore } from "@/stores/auth";

export interface AppPluginOptions {
  languages?: readonly string[];
  fetch?: typeof globalThis.fetch;
  history?: RouterHistory;
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
  handleUnauthorized = () => {
    app.runWithContext(() => useAuthStore()).expire();
    const current = router.currentRoute.value;
    if (current.meta.public !== true) {
      void router.replace({ name: "login", query: { reason: "expired", redirect: current.fullPath } });
    }
  };
  return { router, i18n, pinia, api };
}
