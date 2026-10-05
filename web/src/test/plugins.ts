// Helpers for plugin host tests: fake plugin bundles and a host harness that
// runs the plugin store inside an app context with the real router, i18n and
// an API client over a fake server.
import type { PluginDefinition, PluginLocale, PluginMessages } from "@jelee/plugin-sdk";
import { createPinia } from "pinia";
import { createApp, type App, type Plugin } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { vi, type Mock } from "vitest";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import { createAppI18n } from "@/i18n";
import type { PluginBundle } from "@/plugins/host/catalog";
import { pluginHostKey, usePluginStore } from "@/plugins/host/store";
import { routes } from "@/router/routes";

export type FakeBundle = PluginBundle & { load: Mock<(entry: string) => Promise<unknown>> };

/** The same flat catalog in every language. */
export function sameMessages(messages: PluginMessages): Record<PluginLocale, PluginMessages> {
  return { "zh-CN": messages, "zh-TW": messages, "ja-JP": messages, "en-US": messages };
}

export function testManifest(id: string, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  const text = { "zh-CN": id, "zh-TW": id, "ja-JP": id, "en-US": id };
  return {
    id,
    name: text,
    description: text,
    version: "1.0.0",
    sdkVersion: "^1.0.0",
    minJeleeVersion: "0.1.0",
    permissions: [],
    hooks: ["item.action"],
    dependencies: {},
    entry: "./index.ts",
    ...overrides,
  };
}

/** A bundle whose entry default-exports `definition` (or fails to load). */
export function fakeBundle(manifest: unknown, definition: PluginDefinition | Error, enabledByDefault = true): FakeBundle {
  return {
    manifest,
    official: false,
    enabledByDefault,
    load: vi.fn<(entry: string) => Promise<unknown>>(() => (definition instanceof Error ? Promise.reject(definition) : Promise.resolve({ default: definition }))),
  };
}

export function providePlugins(bundles: readonly PluginBundle[]): Plugin {
  return {
    install(app: App) {
      app.provide(pluginHostKey, { bundles, jeleeVersion: "0.1.0" });
    },
  };
}

/** Runs the plugin store in a bare app context (no components). */
export function pluginHost(bundles: readonly PluginBundle[], fetch: typeof globalThis.fetch = () => Promise.reject(new Error("offline"))) {
  const app = createApp({ render: () => null });
  const api = createApiClient({ fetch, baseUrl: "http://localhost" });
  const router = createRouter({ history: createMemoryHistory(), routes });
  app.provide(apiKey, api);
  app.use(createPinia());
  app.use(createAppI18n("en-US"));
  app.use(router);
  app.use(providePlugins(bundles));
  const store = app.runWithContext(() => usePluginStore());
  return { app, api, router, store };
}
