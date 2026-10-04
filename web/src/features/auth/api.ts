import type { ApiClient } from "@/api/client";
import type { CsrfToken } from "@/api/auth";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";

export type SessionGrant = components["schemas"]["SessionGrant"];
export type User = components["schemas"]["User"];
export type SecondFactorChallenge = components["schemas"]["SecondFactorChallenge"];

/** Sessions created through this endpoint are always "web" sessions server-side. */
export const webDeviceName = "Jelee Web";

/** A session, or for an account with a second factor the challenge of the next step (G07.8). */
export async function login(client: ApiClient, name: string, password: string): Promise<SessionGrant | SecondFactorChallenge> {
  const body = await call(client.POST("/api/v1/auth/login", { body: { name, password, deviceName: webDeviceName } }));
  return body.data;
}

export async function logout(client: ApiClient): Promise<void> {
  await callNoContent(client.POST("/api/v1/auth/logout", { body: {} }));
}

/** Reads the account behind the browser's session cookie, if any. */
export async function currentUser(client: ApiClient): Promise<User> {
  const body = await call(client.GET("/api/v1/users/me", {}));
  return body.data;
}

/** Reads the CSRF token of the cookie session, e.g. after a page reload. */
export async function readCsrf(client: ApiClient): Promise<CsrfToken> {
  const body = await call(client.GET("/api/v1/auth/csrf", {}));
  return body.data;
}
