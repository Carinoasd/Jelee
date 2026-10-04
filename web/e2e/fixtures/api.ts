// A fake Jelee API answered through Playwright request interception: the
// tests need no server process and no database. Unknown endpoints answer 404
// and are recorded, so a page that starts calling something new shows up as
// a test failure instead of a silently different screenshot.
import type { Page, Request, Route } from "@playwright/test";
import * as data from "./data";

export interface FakeApiOptions {
  /** The browser holds a session cookie; false starts at the login page. */
  signedIn?: boolean;
  /** GET /api/v1/setup/status answers 200: the setup wizard is due. */
  setupRequired?: boolean;
  /** GET /api/v1/system reports developer mode as active. */
  devMode?: boolean;
}

export interface FakeApi {
  readonly options: Required<FakeApiOptions>;
  /** Every request the page sent, as "METHOD /path?query". */
  readonly requests: string[];
  /** Requests no fake route answered. */
  readonly unhandled: string[];
  /** Unsafe requests (POST, PUT, PATCH, DELETE) with their JSON bodies. */
  readonly writes: { method: string; path: string; body: unknown }[];
}

const csrf = "c".repeat(43);

const errorBody = (code: string) => ({ error: { code, message: code, details: {}, traceId: "trace-" + code } });

type Answer = { status: number; body?: unknown; headers?: Record<string, string> };

const ok = (body: unknown): Answer => ({ status: 200, body: { data: body } });
const noContent: Answer = { status: 204 };

/** A deterministic poster: a flat colour per item with its initial. */
function posterSvg(itemId: string): string {
  const item = data.items.find((entry) => entry.id === itemId);
  const hue = (Number.parseInt(itemId.slice(-4), 16) * 47) % 360;
  const letter = item?.title.charAt(0) ?? "?";
  return (
    '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="300" viewBox="0 0 200 300">' +
    `<rect width="200" height="300" fill="hsl(${String(hue)} 45% 42%)"/>` +
    `<circle cx="100" cy="120" r="54" fill="hsl(${String(hue)} 50% 62%)"/>` +
    `<text x="100" y="140" font-family="DejaVu Sans, sans-serif" font-size="56" text-anchor="middle" fill="#fff">${letter}</text>` +
    "</svg>"
  );
}

function itemsPage(url: URL): Answer {
  const parentId = url.searchParams.get("parentId");
  const q = url.searchParams.get("q");
  let list = data.items.filter((item) => item.parentId === undefined);
  if (parentId !== null) {
    list = list.filter((item) => item.libraryId === parentId);
  }
  if (q !== null) {
    list = list.filter((item) => item.title.toLowerCase().includes(q.toLowerCase()));
  }
  const sort = url.searchParams.get("sort") ?? "name";
  list = [...list].sort((a, b) =>
    sort === "name" ? a.title.localeCompare(b.title) : (b.premiereDate ?? "").localeCompare(a.premiereDate ?? "") || a.id.localeCompare(b.id),
  );
  if (url.searchParams.get("order") === "desc" && sort === "name") {
    list.reverse();
  }
  const offset = Number(url.searchParams.get("offset") ?? "0");
  const limit = Math.min(Number(url.searchParams.get("limit") ?? "50"), 100);
  return { status: 200, body: { data: list.slice(offset, offset + limit), pagination: { nextCursor: "", limit, offset, total: list.length } } };
}

/** Answers one API request; undefined means no fake route matched. */
function answer(api: FakeApi, method: string, url: URL): Answer | undefined {
  const path = url.pathname;
  const { options } = api;
  const route = method + " " + path;
  if (route === "GET /api/v1/system") {
    const dev = options.devMode ? { devMode: true, devModeExpiresAt: "2026-10-01T18:00:00Z" } : { devMode: false };
    return { status: 200, body: { data: { name: "Jelee", version: "0.1.0", ...dev } }, headers: { "X-Jelee-Dev-Mode": String(options.devMode) } };
  }
  if (route === "GET /api/v1/setup/status") {
    return options.setupRequired ? ok({ setupRequired: true, tokenRequired: true }) : { status: 410, body: errorBody("gone") };
  }
  if (path.startsWith("/api/v1/setup")) {
    return method === "GET" ? ok(data.setupState) : ok({ ...data.setupState, current: "admin" });
  }
  if (route === "POST /api/v1/auth/login") {
    options.signedIn = true;
    return ok({ token: "t".repeat(43), csrf, user: data.adminUser, session: data.sessions[0] });
  }
  if (route === "GET /api/v1/site/appearance") {
    return ok({ defaultTheme: "system", tokens: { light: {}, dark: {} }, css: "", fontHosts: [], defaultLayout: null });
  }
  if (route === "GET /api/v1/site/plugins") {
    return ok({ plugins: [], settings: {} });
  }
  if (!options.signedIn) {
    return { status: 401, body: errorBody("authentication_required") };
  }
  const user = /^\/api\/v1\/users\/([^/]+)(\/.*)?$/.exec(path);
  const item = /^\/api\/v1\/items\/([^/]+)(\/.*)?$/.exec(path);
  const webhook = /^\/api\/v1\/webhooks\/([^/]+)(\/.*)?$/.exec(path);
  switch (route) {
    case "POST /api/v1/auth/logout":
      options.signedIn = false;
      return noContent;
    case "GET /api/v1/auth/csrf":
      return ok({ csrf });
    case "GET /api/v1/users/me":
      return ok(data.adminUser);
    case "GET /api/v1/users/me/preferences":
      return ok(data.preferences);
    case "GET /api/v1/users/me/watch-stats":
    case "GET /api/v1/watch-stats":
      return ok(data.watchStats);
    case "GET /api/v1/users":
      return ok({ users: data.users, pagination: { limit: 50, nextCursor: "" } });
    case "GET /api/v1/libraries":
      return ok({ libraries: data.libraries, pagination: { limit: 50, nextCursor: "" } });
    case "GET /api/v1/items":
      return itemsPage(url);
    case "GET /api/v1/access/policy":
      return ok(data.accessPolicy);
    case "GET /api/v1/access/parental-ratings":
      return ok(data.parentalRatings);
    case "GET /api/v1/access/network-rules":
      return ok(data.networkRules);
    case "GET /api/v1/client-control/policy":
      return ok(data.clientPolicy);
    case "GET /api/v1/client-control/rules":
      return ok(data.clientRules);
    case "GET /api/v1/client-control/clients":
      return ok({ clients: data.knownClients, pagination: { limit: 50, nextCursor: "" } });
    case "GET /api/v1/client-control/hits":
      return ok({ hits: data.clientHits, pagination: { limit: 50, nextCursor: "" } });
    case "GET /api/v1/client-control/stats":
      return ok(data.clientStats);
    case "GET /api/v1/webhooks":
      return ok({ webhooks: data.webhooks, events: data.webhookEvents });
    case "GET /api/v1/shares":
      return ok(data.shares);
    case "GET /api/v1/site/appearance/config":
      return ok(data.appearance);
    case "GET /api/v1/site/plugins/config":
      return ok(data.sitePlugins);
  }
  if (user !== null && method === "GET") {
    const target = data.users.find((entry) => entry.id === user[1]);
    if (target === undefined) {
      return { status: 404, body: errorBody("not_found") };
    }
    switch (user[2] ?? "") {
      case "":
        return ok(target);
      case "/sessions":
        return ok(data.sessions.map((session) => ({ ...session, userId: target.id })));
      case "/app-passwords":
        return ok([]);
      case "/two-factor":
        return ok(data.twoFactor);
      case "/libraries":
        return ok(data.libraries.map((library) => ({ libraryId: library.id, name: library.name })));
      case "/content-access":
        return ok({ blockedTags: [], rules: [] });
      case "/delivery-limits":
        return ok({});
    }
  }
  if (item !== null && method === "GET") {
    const found = data.items.find((entry) => entry.id === item[1]);
    if (found === undefined) {
      return { status: 404, body: errorBody("not_found") };
    }
    switch (item[2] ?? "") {
      case "":
        return ok(found);
      case "/details":
        return ok(found.id === data.detailItem.id ? data.itemDetails : { ...found, genres: [], externalIds: [], nfo: { status: "unread", fields: [] } });
      case "/sources":
        return ok({ itemId: found.id, sources: found.id === data.detailItem.id ? data.itemSources : [] });
      case "/track-preferences":
        return ok({ ...data.trackPreferences, itemId: found.id });
      case "/versions":
        return ok({ ...data.versionOverview, itemId: found.id });
    }
  }
  if (webhook !== null && method === "GET") {
    switch (webhook[2] ?? "") {
      case "":
        return ok(data.webhooks[0]);
      case "/deliveries":
        return ok({ deliveries: data.webhookDeliveries, pagination: { limit: 50, nextCursor: "" } });
    }
  }
  // Unsafe requests the tests drive (two-step confirmations) succeed.
  if (method === "DELETE" || (method === "POST" && /\/(revoke|kick|block|unlock|restore)$/.test(path))) {
    return noContent;
  }
  return undefined;
}

/** Installs the fake API, the poster images and a guard against other requests on page. */
export async function installFakeApi(page: Page, options: FakeApiOptions = {}): Promise<FakeApi> {
  const api: FakeApi = {
    options: { signedIn: true, setupRequired: false, devMode: false, ...options },
    requests: [],
    unhandled: [],
    writes: [],
  };
  const record = (request: Request) => {
    const url = new URL(request.url());
    api.requests.push(request.method() + " " + url.pathname + url.search);
  };
  await page.route("**/api/**", async (route: Route) => {
    const request = route.request();
    record(request);
    const url = new URL(request.url());
    const method = request.method();
    if (!["GET", "HEAD"].includes(method)) {
      let body: unknown;
      try {
        body = request.postDataJSON();
      } catch {
        body = request.postData();
      }
      api.writes.push({ method, path: url.pathname, body });
    }
    const result = answer(api, method, url);
    if (result === undefined) {
      api.unhandled.push(method + " " + url.pathname);
      await route.fulfill({ status: 404, json: errorBody("not_found") });
      return;
    }
    await route.fulfill({
      status: result.status,
      headers: { "Content-Type": "application/json", ...result.headers },
      ...(result.body === undefined ? {} : { body: JSON.stringify(result.body) }),
    });
  });
  await page.route("**/images/**", async (route: Route) => {
    record(route.request());
    const itemId = decodeURIComponent(new URL(route.request().url()).pathname.split("/").at(-1) ?? "");
    await route.fulfill({ status: 200, contentType: "image/svg+xml", body: posterSvg(itemId) });
  });
  return api;
}
