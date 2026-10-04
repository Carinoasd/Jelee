// Minimal in-memory stand-in for the Jelee HTTP API used by component and
// store tests. It answers only the endpoints the web client calls and models
// the browser's cookie jar: after a web login the "browser" holds the
// HttpOnly session cookie, so requests without an Authorization header are
// cookie-authenticated and unsafe methods must carry the CSRF header.
import type { components } from "@/api/schema";

type CatalogItem = components["schemas"]["CatalogItem"];
type ItemDetails = components["schemas"]["CatalogItemDetails"];
type MediaSourceInfo = components["schemas"]["MediaSourceInfo"];
type Session = components["schemas"]["Session"];
type User = components["schemas"]["User"];

export interface FakeServer {
  fetch: typeof globalThis.fetch;
  requests: Request[];
  libraries: { id: string; name: string; roots: number }[];
  items: CatalogItem[];
  /** Extra detail fields per item; genres, IDs and NFO state default to empty. */
  details: Record<string, Partial<ItemDetails>>;
  /** File information per item; an item without an entry has no sources. */
  sources: Record<string, MediaSourceInfo[]>;
  sessions: Session[];
  pageSize: number;
  failLibraries: boolean;
  failItems: boolean;
  failSources: boolean;
  failRevoke: boolean;
  /** Whether the simulated browser currently holds the session cookie. */
  cookie: boolean;
  user: User;
  token: string;
  csrf: string;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const errorBody = (code: string) => ({ error: { code, message: code, details: {}, traceId: "trace-" + code } });

export const userId = "00000000-0000-4000-8000-000000000001";
export const currentSessionId = "00000000-0000-4000-8000-000000000002";

function page<T>(list: readonly T[], url: URL, size: number, id: (value: T) => string) {
  const cursor = url.searchParams.get("cursor");
  const limit = Math.min(Number(url.searchParams.get("limit") ?? size), size);
  const start = cursor === null ? 0 : list.findIndex((entry) => id(entry) === cursor) + 1;
  const slice = list.slice(start, start + limit);
  const last = slice.at(-1);
  const next = start + limit < list.length && last !== undefined ? id(last) : "";
  return { slice, pagination: { limit, nextCursor: next } };
}

export function createFakeServer(): FakeServer {
  const server: FakeServer = {
    requests: [],
    libraries: [],
    items: [],
    details: {},
    sources: {},
    sessions: [],
    pageSize: 50,
    failLibraries: false,
    failItems: false,
    failSources: false,
    failRevoke: false,
    cookie: false,
    token: "t".repeat(43),
    csrf: "c".repeat(43),
    user: {
      id: userId,
      name: "admin",
      displayName: "Admin",
      locale: "ja-JP",
      hidden: false,
      admin: true,
      disabled: false,
      allowNative: true,
      createdAt: "2026-10-01T00:00:00Z",
    },
    fetch: async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      server.requests.push(request.clone());
      const url = new URL(request.url, "http://localhost");
      const path = url.pathname;
      const method = request.method;
      const header = request.headers.get("Authorization");
      const bearer = header === "Bearer " + server.token;
      const viaCookie = header === null && server.cookie;
      const authorized = bearer || viaCookie;
      const unsafe = !["GET", "HEAD"].includes(method);

      if (path === "/api/v1/auth/login" && method === "POST") {
        const body = (await request.json()) as { name: string; password: string };
        if (body.name !== "admin" || body.password !== "correct horse battery") {
          return json(401, errorBody("authentication_required"));
        }
        server.cookie = true;
        const session: Session = {
          id: currentSessionId,
          userId,
          clientKind: "web",
          deviceName: "Jelee Web",
          createdAt: "2026-10-01T00:00:00Z",
          expiresAt: "2026-10-02T00:00:00Z",
        };
        return json(200, { data: { token: server.token, csrf: server.csrf, user: server.user, session } });
      }
      if (!authorized) {
        return json(401, errorBody("authentication_required"));
      }
      if (viaCookie && unsafe && request.headers.get("X-Jelee-CSRF") !== server.csrf) {
        return json(403, errorBody("csrf_failed"));
      }
      if (path === "/api/v1/auth/logout" && method === "POST") {
        server.cookie = false;
        return new Response(null, { status: 204 });
      }
      if (path === "/api/v1/auth/csrf" && method === "GET") {
        return json(200, { data: { csrf: server.csrf } });
      }
      if (path === "/api/v1/users/me" && method === "GET") {
        return json(200, { data: server.user });
      }
      if (path === "/api/v1/libraries" && method === "GET") {
        if (server.failLibraries) {
          return json(503, errorBody("not_ready"));
        }
        const start = Number(url.searchParams.get("cursor") ?? "0");
        const slice = server.libraries.slice(start, start + server.pageSize);
        const next = start + server.pageSize < server.libraries.length ? String(start + server.pageSize) : "";
        return json(200, { data: { libraries: slice, pagination: { limit: server.pageSize, nextCursor: next } } });
      }
      if (path === "/api/v1/items" && method === "GET") {
        if (server.failItems) {
          return json(503, errorBody("not_ready"));
        }
        const parentId = url.searchParams.get("parentId");
        if (parentId === null) {
          const { slice, pagination } = page(server.items, url, server.pageSize, (item) => item.id);
          return json(200, { data: slice, pagination });
        }
        // Offset form, as far as the web client uses it: the top level of a
        // library, sorted by title or year.
        const sort = url.searchParams.get("sort") ?? "name";
        const descending = url.searchParams.get("order") === "desc";
        const matched = server.items
          .filter((item) => item.libraryId === parentId && item.parentId === undefined)
          .sort((a, b) => {
            const order =
              sort === "name" ? a.title.localeCompare(b.title) : (a.productionYear ?? 0) - (b.productionYear ?? 0) || a.id.localeCompare(b.id);
            return descending ? -order : order;
          });
        const offset = Number(url.searchParams.get("offset") ?? "0");
        const limit = Math.min(Number(url.searchParams.get("limit") ?? "50"), server.pageSize);
        return json(200, { data: matched.slice(offset, offset + limit), pagination: { nextCursor: "", limit, offset, total: matched.length } });
      }
      const itemMatch = /^\/api\/v1\/items\/([^/]+)(\/details|\/sources)?$/.exec(path);
      if (itemMatch && method === "GET") {
        const id = itemMatch[1] ?? "";
        const item = server.items.find((entry) => entry.id === id);
        if (item === undefined) {
          return json(404, errorBody("not_found"));
        }
        if (itemMatch[2] === "/details") {
          return json(200, { data: { genres: [], externalIds: [], nfo: { status: "unread", fields: [] }, ...item, ...server.details[id] } });
        }
        if (itemMatch[2] === "/sources") {
          return server.failSources ? json(503, errorBody("not_ready")) : json(200, { data: { itemId: id, sources: server.sources[id] ?? [] } });
        }
        return json(200, { data: item });
      }
      const sessionsMatch = /^\/api\/v1\/users\/([^/]+)\/sessions(?:\/([^/]+))?$/.exec(path);
      if (sessionsMatch) {
        if (sessionsMatch[1] !== server.user.id) {
          return json(403, errorBody("forbidden"));
        }
        const target = sessionsMatch[2];
        if (method === "GET" && target === undefined) {
          return json(200, { data: server.sessions });
        }
        if (method === "DELETE") {
          if (server.failRevoke) {
            return json(503, errorBody("account_busy"));
          }
          const revoked = target === undefined ? server.sessions : server.sessions.filter((s) => s.id === target);
          if (revoked.length === 0) {
            return json(404, errorBody("not_found"));
          }
          server.sessions = server.sessions.filter((s) => !revoked.includes(s));
          if (revoked.some((s) => s.id === currentSessionId)) {
            server.cookie = false;
          }
          return new Response(null, { status: 204 });
        }
      }
      return json(404, errorBody("not_found"));
    },
  };
  return server;
}
