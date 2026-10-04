// Mounts one routed view with the real router table, Pinia, i18n and an API
// client backed by a stub fetch, with a given signed-in user. The navigation
// guard is not installed here; guard behavior has its own tests.
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, type Pinia } from "pinia";
import { defineComponent, h, type App, type Component } from "vue";
import { createMemoryHistory, createRouter, RouterView, type Router } from "vue-router";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import type { components } from "@/api/schema";
import { createAppI18n } from "@/i18n";
import type { Locale } from "@/i18n/locales";
import { routes } from "@/router/routes";
import { useAuthStore } from "@/stores/auth";

type User = components["schemas"]["User"];

export interface MountedView {
  readonly wrapper: VueWrapper;
  readonly router: Router;
  readonly pinia: Pinia;
}

const mounted: VueWrapper[] = [];

/** Unmounts every view mounted since the last call; use in afterEach. */
export function unmountAll(): void {
  for (const wrapper of mounted.splice(0)) {
    wrapper.unmount();
  }
}

export async function mountView(
  path: string,
  options: { fetch: typeof globalThis.fetch; user?: User | null; locale?: Locale; header?: Component },
): Promise<MountedView> {
  const api = createApiClient({ fetch: options.fetch, baseUrl: "http://localhost" });
  const pinia = createPinia();
  const i18n = createAppI18n(options.locale ?? "en-US");
  const router = createRouter({ history: createMemoryHistory(), routes });
  const user = options.user === undefined ? null : options.user;
  const signIn = {
    install(app: App) {
      app.runWithContext(() => {
        useAuthStore().user = user;
      });
    },
  };
  const provideApi = {
    install(app: App) {
      app.provide(apiKey, api);
    },
  };
  await router.push(path);
  await router.isReady();
  const header = options.header;
  const Host = defineComponent({ render: () => (header ? [h(header), h(RouterView)] : h(RouterView)) });
  const wrapper = mount(Host, {
    global: { plugins: [provideApi, pinia, i18n, router, signIn] },
    attachTo: document.body,
  });
  mounted.push(wrapper);
  await flushPromises();
  return { wrapper, router, pinia };
}
