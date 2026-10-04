// @jelee/plugin-sdk: the only module a plugin imports besides "vue"
// (enforced by ESLint for src/plugins/official). It is an in-tree package
// resolved through the "@jelee/plugin-sdk" alias (vite.config.ts,
// tsconfig.app.json) and depends on nothing in the host application.
import { inject, type InjectionKey } from "vue";
import type { HookMap, HookName } from "./hooks";
import type { PluginApi, PluginLocale, PluginManifest, PluginMessages, PluginSettings, PluginUi } from "./types";

export * from "./hooks";
export * from "./types";
export { validateManifest, pluginIdPattern } from "./manifest";
export type { ManifestEnvironment, ManifestIssue, ManifestIssueCode, ManifestResult, ManifestWarning } from "./manifest";
export { compareVersions, parseRange, parseVersion, satisfies } from "./semver";
export { SDK_VERSION, deprecatedHooks, type Deprecation } from "./version";

/** Everything the host hands one plugin; created per plugin and frozen. */
export interface PluginContext {
  readonly id: string;
  readonly manifest: PluginManifest;
  readonly sdkVersion: string;
  readonly jeleeVersion: string;
  /** Adds a contribution; the hook must be listed in the manifest. */
  register<K extends HookName>(hook: K, contribution: HookMap[K]): void;
  readonly api: PluginApi;
  readonly settings: PluginSettings;
  readonly ui: PluginUi;
  /** Current interface language (reactive). */
  readonly locale: () => PluginLocale;
  /** Looks up one of the plugin's own messages in the current language (reactive). */
  readonly t: (key: string, params?: Readonly<Record<string, string | number>>) => string;
}

/** What a plugin entry default-exports. */
export interface PluginDefinition {
  /** Messages per language; every language must have the same keys. */
  readonly messages: Readonly<Record<PluginLocale, PluginMessages>>;
  /** Registers contributions. Must not block: data loads in components. */
  setup(context: PluginContext): void;
}

/** Identity helper that gives plugin entries full type checking. */
export function definePlugin(definition: PluginDefinition): PluginDefinition {
  return definition;
}

/** Provided by the host around every plugin component. */
export const pluginContextKey: InjectionKey<PluginContext> = Symbol("jelee.plugin");

/** The context of the plugin that owns the calling component. */
export function usePlugin(): PluginContext {
  const context = inject(pluginContextKey, null);
  if (context === null) {
    throw new Error("usePlugin() called outside a plugin component");
  }
  return context;
}

/** Thrown when a plugin uses a capability its manifest did not request. */
export class PluginPermissionError extends Error {
  readonly permission: string;
  constructor(permission: string) {
    super("plugin permission required: " + permission);
    this.name = "PluginPermissionError";
    this.permission = permission;
  }
}

/** Failure of a PluginApi call; carries the server's error code only. */
export class PluginApiError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(code: string, status: number) {
    super("plugin API request failed: " + code);
    this.name = "PluginApiError";
    this.code = code;
    this.status = status;
  }
}
