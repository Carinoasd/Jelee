import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

// Site-wide web client settings (G32.4, G33.2–G33.5). Every signed-in user
// reads the effective values; the stored documents with their revision and
// all writes are administrator-only.

export type SiteAppearance = components["schemas"]["SiteAppearance"];
export type SiteAppearanceConfig = components["schemas"]["SiteAppearanceConfig"];
export type SiteAppearanceInput = components["schemas"]["SiteAppearanceInput"];
export type SitePlugins = components["schemas"]["SitePlugins"];
export type SitePluginsConfig = components["schemas"]["SitePluginsConfig"];
export type SitePluginsInput = components["schemas"]["SitePluginsInput"];
export type SiteSettingsDocument = components["schemas"]["SiteSettingsDocument"];
export type SiteSettingsImport = components["schemas"]["SiteSettingsImport"];
export type PageLayout = components["schemas"]["PageLayout"];
export type UserLayout = components["schemas"]["UserLayout"];

export async function getSiteAppearance(client: ApiClient): Promise<SiteAppearance> {
  return (await call(client.GET("/api/v1/site/appearance", {}))).data;
}

export async function getSiteAppearanceConfig(client: ApiClient): Promise<SiteAppearanceConfig> {
  return (await call(client.GET("/api/v1/site/appearance/config", {}))).data;
}

/** Replaces the appearance; 409 conflict when revision is no longer current. */
export async function saveSiteAppearance(client: ApiClient, body: SiteAppearanceInput): Promise<SiteAppearanceConfig> {
  return (await call(client.PUT("/api/v1/site/appearance", { body }))).data;
}

export async function resetSiteAppearance(client: ApiClient): Promise<SiteAppearanceConfig> {
  return (await call(client.POST("/api/v1/site/appearance/reset", { body: {} }))).data;
}

export async function getSitePlugins(client: ApiClient): Promise<SitePlugins> {
  return (await call(client.GET("/api/v1/site/plugins", {}))).data;
}

export async function getSitePluginsConfig(client: ApiClient): Promise<SitePluginsConfig> {
  return (await call(client.GET("/api/v1/site/plugins/config", {}))).data;
}

/** Replaces the plugin configuration; 409 conflict when revision is stale. */
export async function saveSitePlugins(client: ApiClient, body: SitePluginsInput): Promise<SitePluginsConfig> {
  return (await call(client.PUT("/api/v1/site/plugins", { body }))).data;
}

export async function resetSitePlugins(client: ApiClient): Promise<SitePluginsConfig> {
  return (await call(client.POST("/api/v1/site/plugins/reset", { body: {} }))).data;
}

/** Both documents as one importable file (G33.3). */
export async function exportSiteSettings(client: ApiClient): Promise<SiteSettingsDocument> {
  return (await call(client.GET("/api/v1/site/export", {}))).data;
}

export async function importSiteSettings(client: ApiClient, document: SiteSettingsDocument): Promise<SiteSettingsImport> {
  return (await call(client.POST("/api/v1/site/import", { body: document }))).data;
}

/** The input form of a stored appearance: every member, plus the revision it was read at. */
export function appearanceInput(config: SiteAppearanceConfig): SiteAppearanceInput {
  return {
    defaultTheme: config.defaultTheme,
    tokens: config.tokens,
    customCss: config.customCss,
    allowExternalFonts: config.allowExternalFonts,
    fontHosts: config.fontHosts,
    defaultLayout: config.defaultLayout,
    revision: config.revision,
  };
}

/** The effective view of a stored appearance, as the server derives it, given the sanitized CSS. */
export function appearanceView(config: SiteAppearanceConfig, css: string): SiteAppearance {
  return {
    defaultTheme: config.defaultTheme,
    tokens: config.tokens,
    css,
    fontHosts: config.allowExternalFonts ? config.fontHosts : [],
    defaultLayout: config.defaultLayout,
  };
}
