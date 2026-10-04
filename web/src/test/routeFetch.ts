// Route-table fetch stub for view tests: each test registers only the
// endpoints the view under test calls. Unregistered requests answer 404 so a
// view calling something unexpected fails visibly. Requests are recorded with
// their parsed JSON bodies for assertions.
import type { components } from "@/api/schema";

type User = components["schemas"]["User"];

export interface RecordedRequest {
  readonly method: string;
  readonly path: string;
  readonly url: URL;
  readonly body: unknown;
  readonly headers: Headers;
}

export interface RouteContext {
  readonly url: URL;
  readonly params: Readonly<Record<string, string>>;
  readonly body: unknown;
}

export type RouteHandler = (context: RouteContext) => Response | Promise<Response>;

export interface RouteFetch {
  readonly fetch: typeof globalThis.fetch;
  readonly requests: RecordedRequest[];
  /** Registers (or replaces) the handler for a method and a path like /api/v1/users/:id. */
  on(method: string, path: string, handler: RouteHandler): RouteFetch;
  /** Requests matching a method and an exact path, oldest first. */
  calls(method: string, path: string): RecordedRequest[];
}

export const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

export const data = (body: unknown, status = 200) => json(status, { data: body });

export const noContent = () => new Response(null, { status: 204 });

export const apiError = (status: number, code: string) =>
  json(status, { error: { code, message: "raw server text " + code, details: {}, traceId: "trace-" + code } });

export function createRouteFetch(): RouteFetch {
  const routes: { method: string; pattern: RegExp; names: string[]; handler: RouteHandler }[] = [];
  const requests: RecordedRequest[] = [];
  const self: RouteFetch = {
    requests,
    on(method, path, handler) {
      const names: string[] = [];
      const source = path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/\/:([A-Za-z]+)/g, (_match, name: string) => {
        names.push(name);
        return "/([^/]+)";
      });
      const pattern = new RegExp("^" + source + "$");
      const existing = routes.findIndex((route) => route.method === method && route.pattern.source === pattern.source);
      const entry = { method, pattern, names, handler };
      if (existing >= 0) {
        routes[existing] = entry;
      } else {
        routes.push(entry);
      }
      return self;
    },
    calls(method, path) {
      return requests.filter((request) => request.method === method && request.path === path);
    },
    fetch: async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      const url = new URL(request.url, "http://localhost");
      const text = request.body === null ? "" : await request.text();
      let body: unknown = undefined;
      if (text !== "") {
        try {
          body = JSON.parse(text);
        } catch {
          body = text;
        }
      }
      requests.push({ method: request.method, path: url.pathname, url, body, headers: request.headers });
      for (const route of routes) {
        if (route.method !== request.method) {
          continue;
        }
        const match = route.pattern.exec(url.pathname);
        if (match) {
          const params = Object.fromEntries(route.names.map((name, index) => [name, decodeURIComponent(match[index + 1] ?? "")]));
          return route.handler({ url, params, body });
        }
      }
      return apiError(404, "not_found");
    },
  };
  return self;
}

export const adminUser: User = {
  id: "00000000-0000-4000-8000-0000000000a1",
  name: "admin",
  displayName: "Admin",
  locale: "en-US",
  hidden: false,
  admin: true,
  disabled: false,
  allowNative: true,
  createdAt: "2026-10-01T00:00:00Z",
};

export const regularUser: User = {
  ...adminUser,
  id: "00000000-0000-4000-8000-0000000000b2",
  name: "kid",
  displayName: "Kid",
  admin: false,
};

/** Markup that must never appear on any web page (G27). */
export function expectNoPlaybackMarkup(html: string): void {
  const forbidden = /<video|<audio|<source|<track|<object|<embed|<iframe/i;
  if (forbidden.test(html)) {
    throw new Error("page contains media elements");
  }
}
