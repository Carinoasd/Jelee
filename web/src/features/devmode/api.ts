import type { ApiClient } from "@/api/client";

/** Developer mode as the public system information reports it (G45.3). */
export interface DevModeState {
  active: boolean;
  /** RFC 3339 deadline of the session; empty when inactive. */
  expiresAt: string;
}

export const inactiveDevMode: DevModeState = { active: false, expiresAt: "" };

function parse(body: unknown, header: string | null): DevModeState {
  const data: unknown = typeof body === "object" && body !== null && "data" in body ? body.data : null;
  if (typeof data !== "object" || data === null) {
    return { active: header === "true", expiresAt: "" };
  }
  const active = ("devMode" in data && data.devMode === true) || header === "true";
  const expiresAt = "devModeExpiresAt" in data && typeof data.devModeExpiresAt === "string" ? data.devModeExpiresAt : "";
  return { active, expiresAt: active ? expiresAt : "" };
}

/**
 * Reads the developer mode state from GET /api/v1/system, which needs no
 * session. Any failure reads as inactive: the banner must never block the UI.
 */
export async function readDevMode(client: ApiClient): Promise<DevModeState> {
  try {
    const { data, response } = await client.GET("/api/v1/system", {});
    if (!response.ok) {
      return { active: response.headers.get("X-Jelee-Dev-Mode") === "true", expiresAt: "" };
    }
    return parse(data, response.headers.get("X-Jelee-Dev-Mode"));
  } catch {
    return inactiveDevMode;
  }
}
