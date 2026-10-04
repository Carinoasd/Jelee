import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";
import type { User } from "@/features/auth/api";

export type Profile = components["schemas"]["Profile"];

/** New passwords must hold 12 to 1024 bytes of UTF-8 (server rule). */
export const passwordMinBytes = 12;
export const passwordMaxBytes = 1024;

export function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length;
}

/** Replaces the caller's profile; omitted optional fields reset, so send all. */
export async function updateProfile(client: ApiClient, profile: Profile): Promise<User> {
  const body = await call(client.PUT("/api/v1/users/me/profile", { body: profile }));
  return body.data;
}

/**
 * Verifies the current password and replaces it. On success the server
 * revokes every session of the account, this one included.
 */
export async function changePassword(client: ApiClient, oldPassword: string, newPassword: string): Promise<void> {
  await callNoContent(client.PUT("/api/v1/users/me/password", { body: { oldPassword, newPassword } }));
}
