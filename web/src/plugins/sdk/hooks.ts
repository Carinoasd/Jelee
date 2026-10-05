// Typed extension points of @jelee/plugin-sdk (G32.1). Each hook names what
// a plugin contributes and where the host shows it. None of them can start
// media playback: the web client has no player (G27), and route.register
// rejects playback vocabulary in plugin paths.
import type { PluginComponent, PluginItem, PluginPermission, PluginUi } from "./types";

/** Design tokens a theme.token contribution may override (without "--jl-"). */
export const themeTokenNames = [
  "color-bg",
  "color-surface",
  "color-text",
  "color-text-muted",
  "color-border",
  "color-primary",
  "color-primary-text",
  "color-focus",
  "color-badge-bg",
  "color-info-bg",
  "color-chart",
  "radius-sm",
  "radius-md",
  "font-family",
] as const;
export type ThemeTokenName = (typeof themeTokenNames)[number];
export type ThemeTokens = Readonly<Partial<Record<ThemeTokenName, string>>>;

/** A tab in the item detail page; the component receives { item }. */
export interface DetailTabContribution {
  readonly id: string;
  /** Message key of the plugin's catalog. */
  readonly title: string;
  readonly component: PluginComponent;
}

/** A metadata panel in the item detail page; the component receives { item }. */
export interface MetadataPanelContribution {
  readonly id: string;
  readonly title: string;
  readonly component: PluginComponent;
}

/** A button in the item detail page's action bar. */
export interface ItemActionContribution {
  readonly id: string;
  readonly label: string;
  /** Hides the action for items it does not apply to. */
  readonly when?: (item: PluginItem) => boolean;
  readonly run: (item: PluginItem, ui: PluginUi) => void | Promise<void>;
}

/** A section of the user settings page. */
export interface SettingsSectionContribution {
  readonly id: string;
  readonly title: string;
  readonly component: PluginComponent;
}

/** A control in a library's toolbar; the component receives { libraryId }. */
export interface LibraryToolbarContribution {
  readonly id: string;
  readonly component: PluginComponent;
}

/**
 * Design token overrides, evaluated reactively (reading plugin settings in
 * tokens() re-applies them after a change). Values are checked by the same
 * rules as administrator CSS: no url(), no expression(), no markup.
 */
export interface ThemeTokenContribution {
  readonly id: string;
  readonly tokens: () => ThemeTokens;
  /** Overrides for the dark palette; defaults to tokens(). */
  readonly darkTokens?: () => ThemeTokens;
}

/**
 * A page at /x/<plugin id>/<path>. Paths are lowercase segments; the page is
 * rendered inside the plugin's error boundary and needs a session.
 */
export interface RouteContribution {
  readonly path: string;
  readonly title: string;
  readonly component: PluginComponent;
}

/** A command for the command palette. */
export interface CommandContribution {
  readonly id: string;
  readonly label: string;
  readonly keywords?: readonly string[];
  readonly run: (ui: PluginUi) => void | Promise<void>;
}

/** A label and description for a webhook event type the server publishes. */
export interface WebhookEventTypeContribution {
  readonly eventType: string;
  readonly label: string;
  readonly description?: string;
}

/** Every hook and the contribution it accepts. */
export interface HookMap {
  "media.detail.tabs": DetailTabContribution;
  "item.action": ItemActionContribution;
  "settings.section": SettingsSectionContribution;
  "library.toolbar": LibraryToolbarContribution;
  "theme.token": ThemeTokenContribution;
  "route.register": RouteContribution;
  "command.palette": CommandContribution;
  "webhook.eventType": WebhookEventTypeContribution;
  "metadata.panel": MetadataPanelContribution;
}

export type HookName = keyof HookMap;

export const hookNames = [
  "media.detail.tabs",
  "item.action",
  "settings.section",
  "library.toolbar",
  "theme.token",
  "route.register",
  "command.palette",
  "webhook.eventType",
  "metadata.panel",
] as const satisfies readonly HookName[];

/** Permissions a manifest must request before it may declare a hook. */
export const hookPermissions: Readonly<Partial<Record<HookName, PluginPermission>>> = {
  "route.register": "ui.routes",
  "theme.token": "ui.theme",
};

export function isHookName(value: unknown): value is HookName {
  return typeof value === "string" && (hookNames as readonly string[]).includes(value);
}

/** Contribution identifiers and route segments: lowercase words and dashes. */
export const contributionIdPattern = /^[a-z][a-z0-9-]{0,47}$/;

/** Route segments a plugin page may not use (no playback entry, G27.1). */
export const forbiddenRouteSegment = /^(?:play|player|playback|playing|now-playing|stream|streams|subtitles|audio|video|cast|pip|picture-in-picture|theater|watch)$/;

/** Checks a route.register path: "a", "a/b-c"; returns false for anything else. */
export function isPluginRoutePath(path: string): boolean {
  const segments = path.split("/");
  return (
    path.length <= 96 &&
    segments.length <= 4 &&
    segments.every((segment) => contributionIdPattern.test(segment) && !forbiddenRouteSegment.test(segment))
  );
}
