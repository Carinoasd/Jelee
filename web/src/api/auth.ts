// Replaceable credential strategy for the web client (G35.1).
//
// The server currently issues an opaque bearer token in the login response.
// The browser keeps it only in this module's closure: never in localStorage,
// sessionStorage, IndexedDB, cookies written by script, or any reactive store
// that devtools could serialize. Reloading the page therefore ends the
// browser session. When the server adds an httpOnly session cookie with CSRF
// protection, a cookie strategy implementing the same interface replaces
// createMemoryBearerAuth() in api/client.ts without touching callers.
import type { components } from "./schema";

export type SessionGrant = components["schemas"]["SessionGrant"];

export interface AuthStrategy {
  /** Stable identifier for diagnostics; never contains credentials. */
  readonly kind: "memory-bearer" | "cookie-csrf";
  /** Adds credentials to an outgoing request. */
  authorize(request: Request): Request;
  /** Accepts the server's login grant. */
  establish(grant: SessionGrant): void;
  /** Drops all client-side credential material. */
  clear(): void;
  /** Whether a credential is currently held. */
  hasCredential(): boolean;
}

export function createMemoryBearerAuth(): AuthStrategy {
  let token: string | null = null;
  return {
    kind: "memory-bearer",
    authorize(request) {
      if (token !== null) {
        request.headers.set("Authorization", "Bearer " + token);
      }
      return request;
    },
    establish(grant) {
      if (typeof grant.token !== "string" || grant.token.length === 0) {
        throw new TypeError("login grant without token");
      }
      token = grant.token;
    },
    clear() {
      token = null;
    },
    hasCredential() {
      return token !== null;
    },
  };
}
