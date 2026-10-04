import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { SessionGrant } from "./api";

/** One authenticator code or one unused recovery code (G07.8). */
export type SecondFactorProof = { code: string } | { recoveryCode: string };

/**
 * Completes a login that answered a challenge. The grant is the same as a
 * password login's: the server sets the session cookie and returns the CSRF
 * token. Kept apart from api.ts so only the lazy login view carries it.
 */
export async function completeSecondFactor(client: ApiClient, challenge: string, proof: SecondFactorProof): Promise<SessionGrant> {
  const body = await call(client.POST("/api/v1/auth/login/second-factor", { body: { challenge, ...proof } }));
  return body.data;
}

/** Authenticator codes are six digits; spaces are ignored like the server does. */
export function normalizeCode(value: string): string {
  return value.replace(/\s+/g, "");
}

/** Catalog key of what is wrong with an entered factor, or null when it can be sent. */
export function factorProblem(value: string, recovery: boolean): string | null {
  if (recovery) {
    return value.trim() === "" ? "twoFactor.code.recoveryRequired" : null;
  }
  return /^\d{6}$/.test(normalizeCode(value)) ? null : "twoFactor.code.required";
}

export function proofOf(value: string, recovery: boolean): SecondFactorProof {
  return recovery ? { recoveryCode: value.trim() } : { code: normalizeCode(value) };
}
