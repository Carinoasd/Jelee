import type { Middleware } from "openapi-fetch";
import type { ApiClient } from "@/api/client";
import { formatBody, isExcludedPath, maskedHeaders, readOperations, type BuiltRequest, type ConsoleMethod, type ConsoleOperation } from "./spec";

// Network side of the API console (G49.4). Requests go through the app's
// API client, so the session cookie, the CSRF header, Accept-Language and
// the 401 handling are exactly those of every other page; the console never
// reads or holds a credential itself.

interface RawResult {
  data?: unknown;
  error?: unknown;
  response: Response;
}

interface RawInit {
  params?: { query?: Record<string, string> };
  headers?: Record<string, string>;
  body?: unknown;
  parseAs?: "json" | "stream";
  middleware?: Middleware[];
}

type RawRequest = (method: string, url: string, init: RawInit) => Promise<RawResult>;

/**
 * The console calls paths chosen at run time from the instance's own
 * document, including developer-only routes absent from the generated
 * types, so it uses the untyped form of the client's request method.
 */
function raw(client: ApiClient): RawRequest {
  return client.request as unknown as RawRequest;
}

export type ConsoleAccess = "granted" | "inactive" | "denied";

/**
 * Server check: GET /api/v1/dev exists only on a capable instance and
 * answers administrators only. The console opens only for an active session.
 */
export async function checkConsoleAccess(client: ApiClient): Promise<ConsoleAccess> {
  try {
    const { data, response } = await raw(client)("GET", "/api/v1/dev", {});
    if (!response.ok) {
      return "denied";
    }
    const state: unknown = typeof data === "object" && data !== null && "data" in data ? data.data : null;
    return typeof state === "object" && state !== null && "active" in state && state.active === true ? "granted" : "inactive";
  } catch {
    return "denied";
  }
}

/** Reads the running instance's OpenAPI document and the operations it may call. */
export async function loadOperations(client: ApiClient): Promise<ConsoleOperation[]> {
  const { data, response } = await raw(client)("GET", "/api/v1/openapi.json", {});
  if (!response.ok) {
    throw new Error("openapi document unavailable: " + String(response.status));
  }
  return readOperations(data);
}

export interface ConsoleResponse {
  readonly status: number;
  readonly statusText: string;
  readonly durationMs: number;
  /** X-Request-ID, the trace ID of the request (docs/observability.md). */
  readonly traceId: string;
  readonly headers: readonly [string, string][];
  readonly body: ReturnType<typeof formatBody>;
  readonly size: number;
}

const maxShownBytes = 512 * 1024;

async function readBody(response: Response): Promise<{ body: ReturnType<typeof formatBody>; size: number }> {
  const contentType = response.headers.get("Content-Type") ?? "";
  const buffer = await response.arrayBuffer();
  const textual = contentType === "" || /json|xml|^text\//i.test(contentType);
  if (!textual) {
    return { body: { kind: "binary", text: "" }, size: buffer.byteLength };
  }
  const text = new TextDecoder().decode(buffer.byteLength > maxShownBytes ? buffer.slice(0, maxShownBytes) : buffer);
  return { body: formatBody(contentType, text), size: buffer.byteLength };
}

/**
 * Sends one request built by the console and reports the response with
 * credential headers and fields masked.
 */
export async function sendConsoleRequest(client: ApiClient, method: ConsoleMethod, request: BuiltRequest): Promise<ConsoleResponse> {
  if (isExcludedPath(request.path)) {
    throw new Error("refused: direct-delivery route");
  }
  const captured: { response: Response | null } = { response: null };
  const capture: Middleware = {
    onResponse({ response }) {
      captured.response = response.clone();
      return undefined;
    },
  };
  const started = performance.now();
  const result = await raw(client)(method.toUpperCase(), request.path, {
    params: { query: { ...request.query } },
    headers: { Accept: "application/json", ...request.headers },
    ...(request.body === undefined ? {} : { body: request.body }),
    parseAs: "stream",
    middleware: [capture],
  });
  const durationMs = Math.round(performance.now() - started);
  const response = captured.response ?? result.response;
  const { body, size } = method === "head" ? { body: formatBody("", ""), size: 0 } : await readBody(response);
  return {
    status: response.status,
    statusText: response.statusText,
    durationMs,
    traceId: response.headers.get("X-Request-ID") ?? "",
    headers: maskedHeaders(response.headers),
    body,
    size,
  };
}
