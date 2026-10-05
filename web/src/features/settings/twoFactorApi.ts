// Two-factor authentication and application passwords (G07.8). The "me"
// endpoints accept web sessions only; the server checks every rule again.
import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";
import { normalizeCode, type SecondFactorProof } from "@/features/auth/secondFactor";

export type TwoFactorStatus = components["schemas"]["TwoFactorStatus"];
export type TwoFactorEnrollment = components["schemas"]["TwoFactorEnrollment"];
export type AppPassword = components["schemas"]["AppPassword"];
export type NewAppPassword = components["schemas"]["NewAppPassword"];

/** Application password names hold 1 to 128 bytes of UTF-8 (server rule). */
export const appPasswordNameMaxBytes = 128;

/** Name problems the server would reject: none, "required" or "invalid". */
export function appPasswordNameProblem(name: string): "required" | "invalid" | null {
  if (name === "") {
    return "required";
  }
  // eslint-disable-next-line no-control-regex -- control characters are exactly what is refused
  const control = /[\u0000-\u001f\u007f-\u009f]/;
  return new TextEncoder().encode(name).length > appPasswordNameMaxBytes || control.test(name) ? "invalid" : null;
}

export async function getTwoFactorStatus(client: ApiClient, userId: string): Promise<TwoFactorStatus> {
  const body = await call(client.GET("/api/v1/users/{id}/two-factor", { params: { path: { id: userId } } }));
  return body.data;
}

/** Starts (or restarts) enrollment; the secret is returned only here. */
export async function enrollTwoFactor(client: ApiClient): Promise<TwoFactorEnrollment> {
  const body = await call(client.POST("/api/v1/users/me/two-factor/enroll", { body: {} }));
  return body.data;
}

/**
 * Enables the pending factor. Returns the recovery codes, shown once; the
 * server signs out every other session of the account.
 */
export async function confirmTwoFactor(client: ApiClient, code: string): Promise<string[]> {
  const body = await call(client.POST("/api/v1/users/me/two-factor/confirm", { body: { code: normalizeCode(code) } }));
  return body.data.recoveryCodes;
}

/** Replaces every recovery code after a current code verifies. */
export async function regenerateRecoveryCodes(client: ApiClient, code: string): Promise<string[]> {
  const body = await call(client.POST("/api/v1/users/me/two-factor/recovery-codes", { body: { code: normalizeCode(code) } }));
  return body.data.recoveryCodes;
}

export async function disableTwoFactor(client: ApiClient, password: string, proof: SecondFactorProof): Promise<void> {
  await callNoContent(client.POST("/api/v1/users/me/two-factor/disable", { body: { password, ...proof } }));
}

/** Administrator: removes a user's factor and recovery codes. */
export async function resetTwoFactor(client: ApiClient, userId: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/users/{id}/two-factor", { params: { path: { id: userId } } }));
}

export async function listAppPasswords(client: ApiClient, userId: string): Promise<AppPassword[]> {
  const body = await call(client.GET("/api/v1/users/{id}/app-passwords", { params: { path: { id: userId } } }));
  return body.data;
}

/** Creates an application password; the password itself is returned once. */
export async function createAppPassword(client: ApiClient, name: string): Promise<NewAppPassword> {
  const body = await call(client.POST("/api/v1/users/me/app-passwords", { body: { name } }));
  return body.data;
}

/** Revokes the password and every session it issued. */
export async function revokeAppPassword(client: ApiClient, userId: string, appPasswordId: string): Promise<void> {
  await callNoContent(
    client.DELETE("/api/v1/users/{id}/app-passwords/{appPasswordId}", { params: { path: { id: userId, appPasswordId } } }),
  );
}
