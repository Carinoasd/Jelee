import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";

export type SessionGrant = components["schemas"]["SessionGrant"];
export type User = components["schemas"]["User"];

/** Sessions created through this endpoint are always "web" sessions server-side. */
export const webDeviceName = "Jelee Web";

export async function login(client: ApiClient, name: string, password: string): Promise<SessionGrant> {
  const body = await call(client.POST("/api/v1/auth/login", { body: { name, password, deviceName: webDeviceName } }));
  return body.data;
}

export async function logout(client: ApiClient): Promise<void> {
  await callNoContent(client.POST("/api/v1/auth/logout", { body: {} }));
}
