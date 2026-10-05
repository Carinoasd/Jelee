import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import reference from "../../../../api/openapi.json";
import en from "@/i18n/en-US/devconsole.json";
import { navigationGuard } from "@/router/guards";
import { routes } from "@/router/routes";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, createRouteFetch, data, json, regularUser, type RouteFetch } from "@/test/routeFetch";
import {
  buildRequest,
  curlCommand,
  csrfPlaceholder,
  formatBody,
  isDeliveryPath,
  isExcludedPath,
  maskedHeaders,
  readOperations,
  sessionCookiePlaceholder,
  type ConsoleOperation,
} from "./spec";

const text = en.devconsole;
const csrf = "csrf-secret-value-0123456789";
const sessionCookie = "session-secret-value-abcdef";

// A capable instance's document: a read, a write, a dangerous write with
// iUnderstand, developer routes and the delivery routes the console hides.
const devSpec = {
  openapi: "3.1.0",
  paths: {
    "/api/v1/items/{id}": {
      get: { summary: "Read one item", parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }] },
    },
    "/api/v1/items": {
      get: {
        summary: "List items",
        parameters: [
          { name: "limit", in: "query", schema: { type: "integer" } },
          { name: "sort", in: "query", schema: { type: "string", enum: ["name", "added"] } },
          { name: "X-Jelee-Setup-Token", in: "header", schema: { type: "string" } },
        ],
      },
    },
    "/api/v1/webhooks": {
      post: {
        summary: "Create a webhook",
        requestBody: { required: true, content: { "application/json": { schema: { $ref: "#/components/schemas/WebhookInput" } } } },
      },
    },
    "/api/v1/libraries/{id}/probe/rebuild": {
      post: {
        summary: "Rebuild probes",
        requestBody: { required: true, content: { "application/json": { schema: { type: "object", properties: { iUnderstand: { type: "boolean" } } } } } },
      },
    },
    "/api/v1/dev": { get: { summary: "Read the developer mode session", "x-jelee-dev-only": true } },
    "/api/v1/dev/token": { post: { summary: "Issue a token", "x-jelee-dev-only": true } },
    "/api/v1/sources/{id}/stream": { get: { summary: "Deliver the source" }, head: { summary: "Probe the source" } },
    "/api/v1/sources/{id}/subtitles/{trackId}": { get: { summary: "Deliver a subtitle track" } },
    "/api/v1/sources/{id}/audio/{trackId}": { get: { summary: "Deliver an audio track" } },
    "/api/v1/items/{id}/playback": { get: { summary: "Playback information" } },
    "/api/v1/playback/start": { post: { summary: "Report playback start" } },
  },
  components: {
    schemas: {
      WebhookInput: {
        type: "object",
        properties: { name: { type: "string" }, url: { type: "string" }, events: { type: "array", items: { type: "string" } }, enabled: { type: "boolean", default: true } },
      },
    },
  },
};

function operation(id: string): ConsoleOperation {
  const found = readOperations(devSpec).find((candidate) => candidate.id === id);
  if (found === undefined) {
    throw new Error("no operation " + id);
  }
  return found;
}

describe("API console OpenAPI reading (G49.4)", () => {
  it("lists no playback or direct-delivery route of the committed document", () => {
    const operations = readOperations(reference);
    expect(operations.length).toBeGreaterThan(100);
    for (const listed of operations) {
      expect(isDeliveryPath(listed.path), listed.id).toBe(false);
    }
    const paths = new Set(operations.map((listed) => listed.path));
    for (const hidden of [
      "/api/v1/sources/{id}/stream",
      "/api/v1/sources/{id}/subtitles/{trackId}",
      "/api/v1/sources/{id}/audio/{trackId}",
      "/api/v1/items/{id}/playback",
      "/api/v1/items/{id}/playback/check",
      "/api/v1/playback/start",
      "/api/v1/playback/progress",
      "/api/v1/playback/stop",
      "/api/v1/playback/sessions",
    ]) {
      expect(Object.keys(reference.paths)).toContain(hidden);
      expect(paths.has(hidden), hidden).toBe(false);
    }
    // Everything left out is delivery; nothing else disappears.
    for (const path of Object.keys(reference.paths)) {
      expect(paths.has(path) || isDeliveryPath(path), path).toBe(true);
    }
    expect(paths.has("/api/v1/items/{id}/played")).toBe(true);
    expect(paths.has("/api/v1/users/me/playback-history")).toBe(true);
  });

  it("reads parameters, body skeletons and the confirmation level", () => {
    const ids = readOperations(devSpec).map((listed) => listed.id);
    expect(ids).toEqual([
      "GET /api/v1/dev",
      "GET /api/v1/items",
      "GET /api/v1/items/{id}",
      "POST /api/v1/libraries/{id}/probe/rebuild",
      "POST /api/v1/webhooks",
    ]);
    const list = operation("GET /api/v1/items");
    // Credential headers are never offered as parameters.
    expect(list.params.map((param) => param.name)).toEqual(["limit", "sort"]);
    expect(list.params[1]?.options).toEqual(["name", "added"]);
    expect(list.write).toBe(false);
    const create = operation("POST /api/v1/webhooks");
    expect(JSON.parse(create.bodyTemplate)).toEqual({ name: "", url: "", events: [], enabled: true });
    expect(create.write && !create.dangerous).toBe(true);
    const rebuild = operation("POST /api/v1/libraries/{id}/probe/rebuild");
    expect(rebuild.dangerous).toBe(true);
    expect(JSON.parse(rebuild.bodyTemplate)).toEqual({ iUnderstand: false });
    expect(operation("GET /api/v1/dev").devOnly).toBe(true);
  });

  it("keeps parameter values inside their own segment", () => {
    const read = operation("GET /api/v1/items/{id}");
    expect(buildRequest(read, {}, "")).toEqual({ kind: "missing", name: "id" });
    expect(buildRequest(read, { "path:id": ".." }, "")).toEqual({ kind: "segment", name: "id" });
    const built = buildRequest(read, { "path:id": "a/b?c" }, "");
    expect(built).toMatchObject({ path: "/api/v1/items/a%2Fb%3Fc" });
    expect(isExcludedPath("/api/v1/items/x/stream")).toBe(true);
    expect(isExcludedPath("/api/v1/items/x/%73tream")).toBe(true);
    expect(buildRequest(operation("POST /api/v1/webhooks"), {}, "{not json")).toEqual({ kind: "body" });
  });

  it("copies cURL with placeholders and redacted credential fields only", () => {
    const create = operation("POST /api/v1/webhooks");
    const built = buildRequest(create, {}, JSON.stringify({ name: "n", secret: "s3cr3t", password: "hunter2" }));
    if (!("path" in built)) {
      throw new Error("not built");
    }
    const command = curlCommand("https://jelee.example", create.method, built);
    expect(command).toContain("curl -sS -X POST 'https://jelee.example/api/v1/webhooks'");
    expect(command).toContain("Cookie: " + sessionCookiePlaceholder);
    expect(command).toContain("X-Jelee-CSRF: " + csrfPlaceholder);
    expect(command).toContain('"name":"n"');
    expect(command).not.toContain("s3cr3t");
    expect(command).not.toContain("hunter2");
    const read = buildRequest(operation("GET /api/v1/items"), { "query:limit": "5", "query:sort": "it's" }, "");
    if (!("path" in read)) {
      throw new Error("not built");
    }
    const get = curlCommand("https://jelee.example", "get", read);
    expect(get).not.toContain("X-Jelee-CSRF");
    expect(get).toContain("'https://jelee.example/api/v1/items?limit=5&sort=it%27s'");
    const quoted = buildRequest(create, {}, JSON.stringify({ name: "it's" }));
    if (!("path" in quoted)) {
      throw new Error("not built");
    }
    expect(curlCommand("https://jelee.example", "post", quoted)).toContain(`--data-raw '{"name":"it'\\''s"}'`);
  });

  it("masks credential headers and fields in responses", () => {
    const headers = new Headers({ "Set-Cookie": "__Host-jelee_session=abc", "X-Request-ID": "r1", "Content-Type": "application/json" });
    expect(maskedHeaders(headers)).toEqual([
      ["content-type", "application/json"],
      ["set-cookie", "[redacted]"],
      ["x-request-id", "r1"],
    ]);
    const body = formatBody("application/json", JSON.stringify({ data: { csrf: "c", token: "t", nested: [{ apiKey: "k", name: "kept" }] } }));
    expect(JSON.parse(body.text)).toEqual({ data: { csrf: "[redacted]", token: "[redacted]", nested: [{ apiKey: "[redacted]", name: "kept" }] } });
    expect(formatBody("image/png", "x").kind).toBe("binary");
  });
});

describe("API console route guard (G45.8)", () => {
  const router = createRouter({ history: createMemoryHistory(), routes });
  const target = router.resolve("/admin/dev-console?x=1");

  it("is reachable only by administrators while developer mode is on", () => {
    expect(target.name).toBe("admin-dev-console");
    expect(target.meta).toMatchObject({ admin: true, devMode: true });
    expect(navigationGuard(target, { isAuthenticated: false, isAdmin: false })).toEqual({ name: "login", query: { redirect: "/admin/dev-console?x=1" } });
    expect(navigationGuard(target, { isAuthenticated: true, isAdmin: false, devMode: true })).toEqual({ name: "forbidden" });
    expect(navigationGuard(target, { isAuthenticated: true, isAdmin: true })).toEqual({ name: "not-found", params: { pathMatch: ["admin", "dev-console"] } });
    expect(navigationGuard(target, { isAuthenticated: true, isAdmin: true, devMode: false })).toMatchObject({ name: "not-found" });
    expect(navigationGuard(target, { isAuthenticated: true, isAdmin: true, devMode: true })).toBe(true);
    // Other administration pages do not depend on developer mode.
    expect(navigationGuard(router.resolve("/admin/users"), { isAuthenticated: true, isAdmin: true })).toBe(true);
  });
});

describe("API console page (G49.4)", () => {
  let server: RouteFetch;
  let devActive: boolean;
  let clipboard: string[];

  beforeEach(() => {
    devActive = true;
    clipboard = [];
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: (value: string) => {
          clipboard.push(value);
          return Promise.resolve();
        },
      },
    });
    server = createRouteFetch()
      .on("GET", "/api/v1/system", () => data({ devMode: devActive }))
      .on("GET", "/api/v1/dev", () => (devActive ? data({ active: true, ttlSeconds: 3600, toggles: [] }) : json(404, { error: { code: "not_found" } })))
      .on("GET", "/api/v1/openapi.json", () => json(200, devSpec))
      .on("GET", "/api/v1/items/:id", ({ params }) =>
        json(
          200,
          { data: { id: params.id, name: "Item", token: "item-token-value", csrf: csrf } },
          { "X-Request-ID": "0123456789abcdef0123456789abcdef", "Set-Cookie": "__Host-jelee_session=" + sessionCookie },
        ),
      )
      .on("POST", "/api/v1/webhooks", () => json(201, { data: { id: "w1", secret: "whsec-value" } }, { "X-Request-ID": "trace-write" }))
      .on("POST", "/api/v1/libraries/:id/probe/rebuild", () => json(202, { data: {} }, { "X-Request-ID": "trace-rebuild" }));
  });

  afterEach(() => {
    unmountAll();
    Reflect.deleteProperty(navigator, "clipboard");
  });

  async function open() {
    const view = await mountView("/admin/dev-console", { fetch: server.fetch, user: adminUser, csrf });
    await settled(view.wrapper);
    return view.wrapper;
  }

  // The page first loads its lazy messages, then checks with the server.
  async function settled(wrapper: VueWrapper) {
    await vi.waitFor(() => {
      expect(wrapper.find("[data-testid=devconsole-unavailable]").exists() || wrapper.find("select").exists()).toBe(true);
    });
    await flushPromises();
  }

  async function choose(wrapper: VueWrapper, id: string) {
    const select = wrapper.findAll("select").find((candidate) => candidate.findAll("option").some((option) => option.attributes("value") === id));
    if (select === undefined) {
      throw new Error("no option " + id);
    }
    await select.setValue(id);
    await flushPromises();
  }

  function button(wrapper: VueWrapper, label: string) {
    const found = wrapper.findAll("button").find((candidate) => candidate.text() === label);
    if (found === undefined) {
      throw new Error("no button " + label);
    }
    return found;
  }

  it("stays closed when the server does not confirm an active developer mode", async () => {
    devActive = false;
    const wrapper = await open();
    expect(wrapper.find("[data-testid=devconsole-unavailable]").text()).toBe(text.unavailable);
    expect(server.calls("GET", "/api/v1/openapi.json")).toHaveLength(0);
    expect(wrapper.find("[data-testid=devconsole-form]").exists()).toBe(false);
    // The administration navigation does not offer the console either.
    expect(wrapper.findAll("nav a").map((link) => link.text())).not.toContain("API console");
  });

  it("stays closed for a non-administrator even when the page is mounted directly", async () => {
    server.on("GET", "/api/v1/dev", () => json(403, { error: { code: "forbidden" } }));
    const view = await mountView("/admin/dev-console", { fetch: server.fetch, user: regularUser, csrf });
    await settled(view.wrapper);
    expect(view.wrapper.find("[data-testid=devconsole-unavailable]").exists()).toBe(true);
    expect(server.calls("GET", "/api/v1/openapi.json")).toHaveLength(0);
  });

  it("lists operations without delivery routes, sends a read and shows status, trace ID, masked headers and body", async () => {
    const wrapper = await open();
    expect(wrapper.findAll("nav a").map((link) => link.text())).toContain("API console");
    const listed = wrapper.findAll("option").map((option) => option.attributes("value"));
    expect(listed).toContain("GET /api/v1/items/{id}");
    expect(listed.some((value) => value !== undefined && /\/sources\/|playback|\/dev\/token/.test(value))).toBe(false);

    await choose(wrapper, "GET /api/v1/items/{id}");
    await wrapper.find("input[required]").setValue("item-1");
    await wrapper.find("[data-testid=devconsole-form]").trigger("submit");
    await flushPromises();

    const sent = server.calls("GET", "/api/v1/items/item-1");
    expect(sent).toHaveLength(1);
    expect(wrapper.find("[data-testid=devconsole-status]").text()).toBe("200");
    expect(wrapper.find("[data-testid=devconsole-trace]").text()).toBe("0123456789abcdef0123456789abcdef");
    const headers = wrapper.find("[data-testid=devconsole-headers]").text();
    expect(headers).toContain("set-cookie");
    expect(headers).toContain("[redacted]");
    const body = wrapper.find("[data-testid=devconsole-body]").text();
    expect(body).toContain('"name": "Item"');
    const html = wrapper.html();
    for (const secret of [sessionCookie, csrf, "item-token-value"]) {
      expect(html).not.toContain(secret);
    }

    await button(wrapper, text.copyTrace).trigger("click");
    await button(wrapper, text.copyCurl).trigger("click");
    await flushPromises();
    expect(clipboard[0]).toBe("0123456789abcdef0123456789abcdef");
    expect(clipboard[1]).toContain("/api/v1/items/item-1");
    expect(clipboard[1]).toContain(sessionCookiePlaceholder);
    for (const secret of [sessionCookie, csrf]) {
      expect(clipboard.join("\n")).not.toContain(secret);
    }
  });

  it("sends a write only after the two-step confirmation, with the page's CSRF header", async () => {
    const wrapper = await open();
    await choose(wrapper, "POST /api/v1/webhooks");
    // Enter never sends a write.
    await wrapper.find("[data-testid=devconsole-form]").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/webhooks")).toHaveLength(0);

    await button(wrapper, "Send POST").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/webhooks")).toHaveLength(0);
    expect(wrapper.text()).toContain("POST /api/v1/webhooks changes data on this server.");
    await button(wrapper, "Confirm POST").trigger("click");
    await flushPromises();

    const sent = server.calls("POST", "/api/v1/webhooks");
    expect(sent).toHaveLength(1);
    expect(sent[0]?.headers.get("X-Jelee-CSRF")).toBe(csrf);
    expect(sent[0]?.body).toEqual({ name: "", url: "", events: [], enabled: true });
    expect(wrapper.find("[data-testid=devconsole-status]").text()).toBe("201");
    expect(wrapper.find("[data-testid=devconsole-trace]").text()).toBe("trace-write");
    expect(wrapper.find("[data-testid=devconsole-body]").text()).not.toContain("whsec-value");
    expect(wrapper.find("[data-testid=devconsole-curl]").text()).toContain("X-Jelee-CSRF: " + csrfPlaceholder);
    expect(wrapper.html()).not.toContain(csrf);
  });

  it("needs an acknowledgement before a dangerous operation can be confirmed", async () => {
    const wrapper = await open();
    await choose(wrapper, "POST /api/v1/libraries/{id}/probe/rebuild");
    await wrapper.find("input[required]").setValue("lib-1");
    await flushPromises();
    expect(button(wrapper, "Send POST").attributes("disabled")).toBeDefined();
    await wrapper.find("input[type=checkbox]").setValue(true);
    await button(wrapper, "Send POST").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/libraries/lib-1/probe/rebuild")).toHaveLength(0);
    await button(wrapper, "Confirm POST").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/libraries/lib-1/probe/rebuild")).toHaveLength(1);
    expect(wrapper.find("[data-testid=devconsole-trace]").text()).toBe("trace-rebuild");
    // The acknowledgement is spent by the request.
    expect((wrapper.find("input[type=checkbox]").element as HTMLInputElement).checked).toBe(false);
  });

  it("refuses an invalid body and a missing parameter without sending", async () => {
    const wrapper = await open();
    await choose(wrapper, "GET /api/v1/items/{id}");
    expect(wrapper.text()).toContain("Enter a value for id.");
    expect(wrapper.find("[data-testid=devconsole-send]").attributes("disabled")).toBeDefined();
    await choose(wrapper, "POST /api/v1/webhooks");
    await wrapper.find("textarea").setValue("{broken");
    expect(wrapper.text()).toContain(text.problems.body);
    expect(wrapper.find("textarea").attributes("aria-invalid")).toBe("true");
    expect(server.requests.filter((request) => request.method !== "GET")).toHaveLength(0);
  });

  it("reports a network failure", async () => {
    const wrapper = await open();
    await choose(wrapper, "GET /api/v1/items");
    server.on("GET", "/api/v1/items", () => Promise.reject(new Error("offline")));
    await wrapper.find("[data-testid=devconsole-form]").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain(text.networkError);
  });
});
