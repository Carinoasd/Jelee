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
    onResponse({ response }) {
      if (response.status === 401 && auth.hasCredential()) {
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
