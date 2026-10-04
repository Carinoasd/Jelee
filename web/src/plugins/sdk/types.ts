// Public types of @jelee/plugin-sdk. They describe what a plugin may see:
// copies of catalog data, never API clients, credentials or stores.
import type { Component } from "vue";

/** Interface languages; a plugin ships messages for all four. */
export const pluginLocales = ["zh-CN", "zh-TW", "ja-JP", "en-US"] as const;
export type PluginLocale = (typeof pluginLocales)[number];

/** Text given in every interface language. */
export type LocalizedText = Readonly<Record<PluginLocale, string>>;

/** Flat message catalog of one plugin in one language; "{name}" placeholders. */
export type PluginMessages = Readonly<Record<string, string>>;

/**
 * Permissions a plugin may request. Each one opens one narrow capability of
 * the plugin context; nothing grants writes to the server.
 */
export const pluginPermissions = ["catalog.read", "user.read", "settings.storage", "ui.routes", "ui.theme"] as const;
export type PluginPermission = (typeof pluginPermissions)[number];

/** What a plugin declares before any of its code runs (G32.2). */
export interface PluginManifest {
  /** Lowercase dotted identifier, e.g. "jelee.item-facts". */
  readonly id: string;
  readonly name: LocalizedText;
  readonly description: LocalizedText;
  /** Plugin version (SemVer). */
  readonly version: string;
  /** SDK versions the plugin works with (SemVer range, e.g. "^1.0.0"). */
  readonly sdkVersion: string;
  /** Oldest Jelee version the plugin supports (SemVer). */
  readonly minJeleeVersion: string;
  readonly permissions: readonly PluginPermission[];
  /** Hooks the plugin registers; registering any other hook fails. */
  readonly hooks: readonly string[];
  /** Other plugins this one needs: plugin ID to SemVer range. */
  readonly dependencies: Readonly<Record<string, string>>;
  /** Module next to the manifest that default-exports the plugin, e.g. "./index.ts". */
  readonly entry: string;
  readonly author?: string;
}

/** A catalog entry as plugins see it: a read-only copy of display data. */
export interface PluginItem {
  readonly id: string;
  readonly libraryId: string;
  readonly kind: string;
  readonly title: string;
  readonly originalTitle: string | null;
  readonly productionYear: number | null;
  readonly hasOverview: boolean;
  readonly genres: readonly string[];
  readonly externalIds: readonly { readonly type: string; readonly value: string }[];
  readonly nfoStatus: string;
}

/** File facts of one version of an item; never a path or delivery address. */
export interface PluginSourceSummary {
  readonly container: string;
  readonly sizeBytes: number | null;
  readonly durationSeconds: number | null;
  readonly width: number | null;
  readonly height: number | null;
}

export interface PluginLibrary {
  readonly id: string;
  readonly name: string;
}

/** The signed-in account, without session or credential data. */
export interface PluginUser {
  readonly name: string;
  readonly displayName: string;
  readonly locale: string;
  readonly admin: boolean;
}

/**
 * Read-only data access for plugins. Every method needs the permission named
 * in its comment and only reaches the listed endpoint; there is no generic
 * request method, no write, and no access to tokens or CSRF values.
 */
export interface PluginApi {
  /** catalog.read: GET /api/v1/items/{id}/details */
  item(id: string): Promise<PluginItem>;
  /** catalog.read: GET /api/v1/items/{id}/sources */
  itemSources(id: string): Promise<readonly PluginSourceSummary[]>;
  /** catalog.read: GET /api/v1/libraries (first page) */
  libraries(): Promise<readonly PluginLibrary[]>;
  /** user.read: GET /api/v1/users/me */
  me(): Promise<PluginUser>;
}

/** JSON values a plugin may keep in its settings namespace. */
export type PluginSettingValue = string | number | boolean | null | readonly PluginSettingValue[] | { readonly [key: string]: PluginSettingValue };

/**
 * Settings namespace of one plugin (needs settings.storage). Values are
 * reactive: a component reading get() re-renders after set().
 */
export interface PluginSettings {
  get<T extends PluginSettingValue>(key: string, fallback: T): T;
  set(key: string, value: PluginSettingValue): void;
  remove(key: string): void;
  keys(): readonly string[];
}

export type NoticeTone = "info" | "success" | "danger";

/** Host user-interface services. */
export interface PluginUi {
  /** Shows a toast with one of the plugin's own messages. */
  notify(key: string, tone?: NoticeTone, params?: Readonly<Record<string, string | number>>): void;
}

/**
 * Lazy loader of a component, e.g. () => import("./Tab.vue"). Plugin
 * components are always split from the main bundle and loaded on first use.
 */
export type PluginComponent = () => Promise<Component | { default: Component }>;
