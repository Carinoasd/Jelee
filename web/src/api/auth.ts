// Replaceable credential strategy for the web client (G35.1).
//
// The default strategy relies on the server's HttpOnly, Secure,
// SameSite=Strict __Host-jelee_session cookie: script never sees the session
// token. The login response still carries an opaque bearer token for API
// compatibility; the cookie strategy discards it without storing it anywhere.
// What the page keeps is the CSRF token, held only in this module's closure
// (never in localStorage, sessionStorage, IndexedDB, script-written cookies
// or any reactive store that devtools could serialize) and sent as
// X-Jelee-CSRF on unsafe methods. After a reload the app resumes the cookie
// session through GET /api/v1/auth/csrf.
//
// createMemoryBearerAuth() remains for development setups where a browser
// refuses Secure cookies on plain-HTTP loopback (see docs/security-model.md).
import type { components } from "./schema";

export type SessionGrant = components["schemas"]["SessionGrant"];
export type CsrfToken = components["schemas"]["CSRFToken"];

export const csrfHeader = "X-Jelee-CSRF";

export interface AuthStrategy {
  /** Stable identifier for diagnostics; never contains credentials. */
  readonly kind: "memory-bearer" | "cookie-csrf";
  /** Whether a session can outlive a page reload and should be resumed at start. */
  readonly survivesReload: boolean;
  /** Adds credentials to an outgoing request. */
  authorize(request: Request): Request;
  /** Accepts the server's login grant. */
  establish(grant: SessionGrant): void;
  /** Resumes a session that survived a reload, using a freshly read CSRF token. */
  resume(token: CsrfToken): void;
  /** Drops all client-side credential material. */
  clear(): void;
  /** Whether a credential is currently held. */
  hasCredential(): boolean;
}

const safeMethods = new Set(["GET", "HEAD", "OPTIONS", "TRACE"]);

function validCsrf(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

export function createCookieCsrfAuth(): AuthStrategy {
  let csrf: string | null = null;
  return {
    kind: "cookie-csrf",
    survivesReload: true,
    authorize(request) {
      // The browser attaches the HttpOnly cookie itself (same-origin only).
      if (csrf !== null && !safeMethods.has(request.method.toUpperCase())) {
        request.headers.set(csrfHeader, csrf);
      }
      return request;
    },
    establish(grant) {
      // grant.token is deliberately ignored: the cookie is the credential.
      if (!validCsrf(grant.csrf)) {
        throw new TypeError("login grant without csrf token");
      }
      csrf = grant.csrf;
    },
    resume(token) {
      if (!validCsrf(token.csrf)) {
        throw new TypeError("csrf response without token");
      }
      csrf = token.csrf;
    },
    clear() {
      csrf = null;
    },
    hasCredential() {
      return csrf !== null;
    },
  };
}

export function createMemoryBearerAuth(): AuthStrategy {
  let token: string | null = null;
  return {
    kind: "memory-bearer",
    survivesReload: false,
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
    resume() {
      throw new TypeError("memory bearer sessions end with the page");
    },
    clear() {
      token = null;
    },
    hasCredential() {
      return token !== null;
    },
  };
}
