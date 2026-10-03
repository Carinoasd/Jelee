// Minimal in-memory stand-in for the Jelee HTTP API used by component and
// store tests. It answers only the endpoints the web skeleton calls.
export interface FakeServer {
  fetch: typeof globalThis.fetch;
  requests: Request[];
  libraries: { id: string; name: string; roots: number }[];
  pageSize: number;
  failLibraries: boolean;
  token: string;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const errorBody = (code: string) => ({ error: { code, message: code, details: {}, traceId: "trace-" + code } });

export function createFakeServer(): FakeServer {
  const server: FakeServer = {
    requests: [],
    libraries: [],
    pageSize: 50,
    failLibraries: false,
    token: "t".repeat(43),
    fetch: async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      server.requests.push(request.clone());
      const url = new URL(request.url, "http://localhost");
      const authorized = request.headers.get("Authorization") === "Bearer " + server.token;
      if (url.pathname === "/api/v1/auth/login" && request.method === "POST") {
        const body = (await request.json()) as { name: string; password: string };
        if (body.name !== "admin" || body.password !== "correct horse battery") {
          return json(401, errorBody("authentication_required"));
        }
        return json(200, {
          data: {
            token: server.token,
            user: {
              id: "00000000-0000-4000-8000-000000000001",
              name: "admin",
              displayName: "Admin",
              locale: "ja-JP",
              hidden: false,
              admin: true,
              disabled: false,
              createdAt: "2026-10-01T00:00:00Z",
            },
            session: {
              id: "00000000-0000-4000-8000-000000000002",
              userId: "00000000-0000-4000-8000-000000000001",
              clientKind: "web",
              deviceName: "Jelee Web",
              createdAt: "2026-10-01T00:00:00Z",
              expiresAt: "2026-10-02T00:00:00Z",
            },
          },
        });
      }
      if (url.pathname === "/api/v1/auth/logout" && request.method === "POST") {
        return authorized ? new Response(null, { status: 204 }) : json(401, errorBody("authentication_required"));
      }
      if (url.pathname === "/api/v1/libraries" && request.method === "GET") {
        if (!authorized) {
          return json(401, errorBody("authentication_required"));
        }
        if (server.failLibraries) {
          return json(503, errorBody("not_ready"));
        }
        const start = Number(url.searchParams.get("cursor") ?? "0");
        const page = server.libraries.slice(start, start + server.pageSize);
        const next = start + server.pageSize < server.libraries.length ? String(start + server.pageSize) : "";
        return json(200, { data: { libraries: page, pagination: { limit: server.pageSize, nextCursor: next } } });
      }
      return json(404, errorBody("not_found"));
    },
  };
  return server;
}
