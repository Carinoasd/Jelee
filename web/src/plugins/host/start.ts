// Starts the extension host and administrator CSS. Loaded as a separate
// chunk after the core plugins, so neither the SDK, the manifest validator
// nor the CSS sanitizer weighs on the main bundle (G35.4).
import { parseVersion } from "@jelee/plugin-sdk";
import type { App } from "vue";
import type { ApiClient } from "@/api/client";
import { useAppearanceStore } from "@/stores/appearance";
import { jeleeVersion, serverVersionKey, usePluginStore } from "./store";

/**
 * The server's version (GET /api/v1/system, no session needed). Plugins'
 * minJeleeVersion is checked against the server that runs; this web
 * client's own version stands in when the server does not say.
 */
export async function readServerVersion(client: ApiClient): Promise<string> {
  try {
    const { data, response } = await client.GET("/api/v1/system", {});
    const body: unknown = data;
    const payload = typeof body === "object" && body !== null && "data" in body ? body.data : null;
    const version = typeof payload === "object" && payload !== null && "version" in payload ? payload.version : null;
    return response.ok && typeof version === "string" && parseVersion(version) !== null ? version : jeleeVersion;
  } catch {
    return jeleeVersion;
  }
}

export async function startExtensions(app: App, client: ApiClient) {
  app.provide(serverVersionKey, await readServerVersion(client));
  return app.runWithContext(() => {
    useAppearanceStore();
    return usePluginStore();
  });
}
