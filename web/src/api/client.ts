import createClient, { type Middleware } from "openapi-fetch";
import { createCookieCsrfAuth, type AuthStrategy } from "./auth";
import type { paths } from "./schema";

/**
 * Paths the web client may call. Direct-delivery endpoints (the source stream
 * and its external subtitle and audio tracks) are reserved for authorized
 * native clients (G27.3, G10.9); removing them here makes any web call to them
 * a compile-time error in addition to the server's explicit rejection.
 */
export type WebPaths = Omit<paths, Extract<keyof paths, `${string}/stream` | `/api/v1/sources/${string}`>>;

/**
 * Endpoints whose 401 answers a wrong credential typed by the user rather
 * than an expired session: the server reports a wrong current password on
 * PUT /api/v1/users/me/password as authentication_required. Treating it as
 * expiry would sign the user out for a typo, so the caller handles it.
 */
const credentialCheckPaths: ReadonlySet<string> = new Set(["/api/v1/users/me/password"]);

export interface ApiClientOptions {
  baseUrl?: string;
  auth?: AuthStrategy;
  fetch?: typeof globalThis.fetch;
  /** Current UI locale; sent as Accept-Language so server messages match. */
  locale?: () => string;
  /** Called when the server rejects the credential (HTTP 401). */
  onUnauthorized?: () => void;
}

export function createApiClient(options: ApiClientOptions = {}) {
  const auth = options.auth ?? createCookieCsrfAuth();
  const client = createClient<WebPaths>({
    // Same-origin API by default; an absolute base keeps Request construction
    // valid outside browsers (tests) as well.
    baseUrl: options.baseUrl ?? globalThis.location.origin,
    // The session cookie is same-origin only; never send ambient credentials
    // cross-origin.
    credentials: "same-origin",
    ...(options.fetch ? { fetch: options.fetch } : {}),
  });
  const middleware: Middleware = {
    onRequest({ request }) {
      if (options.locale) {
        request.headers.set("Accept-Language", options.locale());
      }
      return auth.authorize(request);
    },
    onResponse({ response, schemaPath }) {
      if (response.status === 401 && auth.hasCredential() && !credentialCheckPaths.has(schemaPath)) {
        auth.clear();
        options.onUnauthorized?.();
      }
      return response;
    },
  };
  client.use(middleware);
  return { client, auth };
}

export type ApiClient = ReturnType<typeof createApiClient>["client"];
