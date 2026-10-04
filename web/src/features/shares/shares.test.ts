import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp } from "vue";
import { createMemoryHistory } from "vue-router";
import type { components } from "@/api/schema";
import App from "@/App.vue";
import errorsEn from "@/i18n/en-US/errors.json";
import en from "@/i18n/en-US/shares.json";
import { installAppPlugins } from "@/plugins";
import { button, toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, json, regularUser, type RouteFetch } from "@/test/routeFetch";

type Share = components["schemas"]["Share"];
type ShareAccessRecord = components["schemas"]["ShareAccessRecord"];
type User = components["schemas"]["User"];

const text = en.shares;
const libraryId = "10000000-0000-4000-8000-00000000000a";
const shareId = "60000000-0000-4000-8000-000000000001";

const share: Share = {
  id: shareId,
  libraryId,
  libraryName: "Movies",
  expiresAt: "2026-10-11T00:00:00Z",
  readOnly: true,
  allowPlayback: false,
  maxStreams: 1,
  note: "",
  createdAt: "2026-10-04T00:00:00Z",
  state: "active",
  activeSessions: 2,
};

const guestUser: User = {
  ...regularUser,
  id: "00000000-0000-4000-8000-0000000000c3",
  name: "share:" + shareId,
  displayName: "",
};

const guestShare = {
  id: shareId,
  libraryId,
  libraryName: "Movies",
  expiresAt: "2026-10-11T00:00:00Z",
  readOnly: true,
  allowPlayback: false,
};

const items = [
  { id: "20000000-0000-4000-8000-000000000001", libraryId, kind: "Movie", title: "Arrival" },
  { id: "20000000-0000-4000-8000-000000000002", libraryId, kind: "Movie", title: "Contact" },
];

function record(id: number, event: ShareAccessRecord["event"], extra: Partial<ShareAccessRecord> = {}): ShareAccessRecord {
  return { id, event, occurredAt: "2026-10-04T10:00:00Z", ...extra };
}

function adminServer(): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/shares", () => data([share]))
    .on("GET", "/api/v1/libraries", () => data({ libraries: [{ id: libraryId, name: "Movies", roots: 1 }], pagination: { limit: 50, nextCursor: "" } }));
}

function grantFor(user: User) {
  return {
    token: "opaque-bearer",
    csrf: "csrf-guest",
    user,
    session: { id: "30000000-0000-4000-8000-000000000009", userId: user.id, clientKind: "web", deviceName: "Jelee Web", createdAt: "2026-10-04T00:00:00Z", expiresAt: "2026-10-05T00:00:00Z" },
  };
}

function guestRoutes(server: RouteFetch): RouteFetch {
  return server
    .on("GET", "/api/v1/shares/current", () => data(guestShare))
    .on("GET", "/api/v1/items", () => json(200, { data: items, pagination: { limit: 60, nextCursor: "", offset: 0, total: items.length } }));
}

afterEach(() => {
  unmountAll();
  globalThis.history.replaceState(null, "", "/");
});

describe("share links: administration", () => {
  it("creates a link and shows its one-time URL until it is closed", async () => {
    const created: Share = { ...share, id: "60000000-0000-4000-8000-000000000002", activeSessions: 0, createdAt: "2026-10-04T01:00:00Z" };
    const server = adminServer().on("POST", "/api/v1/shares", () => data({ share: created, token: "tok_EN-1" }, 201));
    const { wrapper } = await mountView("/admin/shares", { fetch: server.fetch, user: adminUser });
    expect(wrapper.find("h1").text()).toBe(text.title);
    await button(wrapper, text.create.open).trigger("click");
    await flushPromises();

    // Nothing is sent until a library is chosen.
    await wrapper.find("form").trigger("submit");
    expect(server.calls("POST", "/api/v1/shares")).toHaveLength(0);
    expect(wrapper.text()).toContain(text.create.invalidLibrary);

    const library = wrapper.findAll("select").find((select) => select.findAll("option").some((option) => option.text() === "Movies"));
    await library!.setValue(libraryId);
    await wrapper.find("form").trigger("submit");
    await flushPromises();

    const body = server.calls("POST", "/api/v1/shares")[0]!.body as Record<string, unknown>;
    expect(body).toMatchObject({ libraryId, readOnly: true, allowPlayback: false });
    expect(body).not.toHaveProperty("itemId");
    expect(body).not.toHaveProperty("maxStreams");
    const lifetime = Date.parse(body.expiresAt as string) - Date.now();
    expect(lifetime).toBeGreaterThan(6.9 * 24 * 3600 * 1000);
    expect(lifetime).toBeLessThanOrEqual(7 * 24 * 3600 * 1000);

    const grant = wrapper.find("[data-testid='share-grant']");
    expect(grant.exists()).toBe(true);
    expect(grant.find<HTMLInputElement>("input").element.value).toBe(`${globalThis.location.origin}/share#tok_EN-1`);
    expect(grant.text()).toContain(text.grant.warning);
    expect(toastKeys(wrapper)).toContain("shares.created");
    expect(wrapper.findAll("tbody tr")).toHaveLength(2);

    await button(wrapper, text.grant.close).trigger("click");
    expect(wrapper.find("[data-testid='share-grant']").exists()).toBe(false);
    expect(wrapper.html()).not.toContain("tok_EN-1");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("sends the native options and an item scope", async () => {
    const itemId = "20000000-0000-4000-8000-0000000000aa";
    const server = adminServer().on("POST", "/api/v1/shares", () => data({ share: { ...share, itemId }, token: "t" }, 201));
    const { wrapper } = await mountView("/admin/shares", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.create.open).trigger("click");
    await flushPromises();
    const scope = wrapper.findAll("select").find((select) => select.findAll("option").some((option) => option.attributes("value") === "item"));
    await scope!.setValue("item");
    const inputs = () => wrapper.findAll<HTMLInputElement>("form input");
    await inputs().find((input) => input.attributes("maxlength") === "36")!.setValue(itemId.toUpperCase());
    const native = inputs().filter((input) => input.attributes("type") === "checkbox")[1]!;
    await native.setValue(true);
    expect(wrapper.text()).toContain(text.create.allowNativeHint);
    await inputs().find((input) => input.attributes("inputmode") === "numeric")!.setValue("17");
    await wrapper.find("form").trigger("submit");
    expect(server.calls("POST", "/api/v1/shares")).toHaveLength(0);
    expect(wrapper.text()).toContain("Enter a whole number from 1 to 16.");
    await inputs().find((input) => input.attributes("inputmode") === "numeric")!.setValue("3");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/shares")[0]!.body).toMatchObject({ itemId, allowPlayback: true, maxStreams: 3, readOnly: true });
  });

  it("revokes a link only after the confirmation", async () => {
    const server = adminServer().on("POST", "/api/v1/shares/:id/revoke", () =>
      data({ ...share, state: "revoked", activeSessions: 0, revokedAt: "2026-10-04T12:00:00Z" }),
    );
    const { wrapper } = await mountView("/admin/shares", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.list.revoke).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `/api/v1/shares/${shareId}/revoke`)).toHaveLength(0);
    expect(wrapper.text()).toContain(text.list.revokePrompt.replace("{count}", "2"));
    await button(wrapper, text.list.revokeConfirm).trigger("click");
    await flushPromises();
    const calls = server.calls("POST", `/api/v1/shares/${shareId}/revoke`);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.body).toEqual({});
    expect(wrapper.find("tbody").text()).toContain(text.state.revoked);
    expect(wrapper.findAll("button").some((candidate) => candidate.text() === text.list.revoke)).toBe(false);
    expect(toastKeys(wrapper)).toContain("shares.list.revoked");
  });

  it("pages through the access log with the cursor", async () => {
    const server = adminServer().on("GET", "/api/v1/shares/:id/access", ({ url }) =>
      url.searchParams.get("cursor") === "c2"
        ? data({ records: [record(1, "share.created")], pagination: { limit: 50, nextCursor: "" } })
        : data({
            records: [
              record(3, "share.accessed", { route: "GET /api/v1/items/{id}", clientKind: "web", deviceName: "Jelee Web", ip: "198.51.100.7" }),
              record(2, "share.redeem_refused", { reason: "session_limit" }),
            ],
            pagination: { limit: 50, nextCursor: "c2" },
          }),
    );
    const { wrapper } = await mountView("/admin/shares", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.list.accessLog).trigger("click");
    await flushPromises();
    const log = () => wrapper.findAll("table")[1]!;
    expect(log().findAll("tbody tr")).toHaveLength(2);
    expect(log().text()).toContain("GET /api/v1/items/{id}");
    expect(log().text()).toContain("Jelee Web · Web · 198.51.100.7");
    expect(log().text()).toContain(text.access.reason.sessionLimit);
    expect(server.calls("GET", `/api/v1/shares/${shareId}/access`)[0]!.url.searchParams.get("cursor")).toBeNull();

    await button(wrapper, "Load more").trigger("click");
    await flushPromises();
    expect(server.calls("GET", `/api/v1/shares/${shareId}/access`)[1]!.url.searchParams.get("cursor")).toBe("c2");
    expect(log().findAll("tbody tr")).toHaveLength(3);
    expect(log().text()).toContain(text.access.event.created);
    expect(wrapper.findAll("button").some((candidate) => candidate.text() === "Load more")).toBe(false);
  });
});

describe("share links: guests", () => {
  beforeEach(() => {
    globalThis.history.replaceState(null, "", "/share#tok-1");
  });

  it("redeems the token from the fragment, clears it and shows the share", async () => {
    const server = guestRoutes(createRouteFetch()).on("POST", "/api/v1/shares/redeem", () => data(grantFor(guestUser)));
    const { wrapper, router } = await mountView("/share", { fetch: server.fetch });
    // The fragment is gone before anything else happens.
    expect(globalThis.location.hash).toBe("");
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("shared");
    });
    await vi.waitFor(() => {
      expect(wrapper.find("[data-testid='guest-share']").exists()).toBe(true);
    });
    expect(server.calls("POST", "/api/v1/shares/redeem")[0]!.body).toEqual({ token: "tok-1", deviceName: "Jelee Web" });
    expect(wrapper.text()).toContain("Shared library: Movies");
    expect(wrapper.findAll(".jl-guest__title").map((title) => title.text())).toEqual(["Arrival", "Contact"]);
    expect(wrapper.findAll("a[href^='/items/']")).toHaveLength(2);
    expect(server.calls("GET", "/api/v1/items")[0]!.url.searchParams.get("parentId")).toBe(libraryId);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows share_unavailable and session_limit as localized messages", async () => {
    let code = "share_unavailable";
    const server = createRouteFetch().on("POST", "/api/v1/shares/redeem", () => apiError(code === "share_unavailable" ? 404 : 429, code));
    const first = await mountView("/share", { fetch: server.fetch });
    await vi.waitFor(() => {
      expect(first.wrapper.find("[data-testid='redeem-error']").exists()).toBe(true);
    });
    expect(first.wrapper.text()).toContain(errorsEn.errors.shareUnavailable);
    expect(first.wrapper.text()).toContain("trace-share_unavailable");
    expect(first.wrapper.text()).not.toContain("raw server text");
    expect(first.router.currentRoute.value.name).toBe("share-redeem");
    unmountAll();

    code = "session_limit";
    globalThis.history.replaceState(null, "", "/share#tok-2");
    const second = await mountView("/share", { fetch: server.fetch });
    await vi.waitFor(() => {
      expect(second.wrapper.find("[data-testid='redeem-error']").exists()).toBe(true);
    });
    expect(second.wrapper.text()).toContain(text.redeem.failed);
    expect(server.calls("POST", "/api/v1/shares/redeem")[1]!.body).toMatchObject({ token: "tok-2" });
  });

  it("explains a link without a token and sends nothing", async () => {
    globalThis.history.replaceState(null, "", "/share");
    const server = createRouteFetch();
    const { wrapper } = await mountView("/share", { fetch: server.fetch });
    expect(wrapper.text()).toContain(text.redeem.missing);
    expect(server.requests).toHaveLength(0);
  });

  it("asks a signed-in user before replacing the session", async () => {
    const server = guestRoutes(createRouteFetch())
      .on("POST", "/api/v1/shares/redeem", () => data(grantFor(guestUser)))
      .on("POST", "/api/v1/auth/logout", () => new Response(null, { status: 204 }));
    const { wrapper, router } = await mountView("/share", { fetch: server.fetch, user: regularUser, csrf: "csrf-user" });
    expect(wrapper.text()).toContain("You are signed in as Kid.");
    expect(server.calls("POST", "/api/v1/shares/redeem")).toHaveLength(0);
    await button(wrapper, text.redeem.continue).trigger("click");
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("shared");
    });
    expect(server.calls("POST", "/api/v1/auth/logout")).toHaveLength(1);
    expect(server.calls("POST", "/api/v1/shares/redeem")[0]!.body).toMatchObject({ token: "tok-1" });
  });
});

describe("share links: guest item page", () => {
  it("names the share in the breadcrumbs and never lists libraries", async () => {
    const episode = { id: "20000000-0000-4000-8000-0000000000e1", libraryId, parentId: "20000000-0000-4000-8000-0000000000d1", kind: "Episode", title: "Pilot" };
    const server = createRouteFetch()
      .on("GET", "/api/v1/shares/current", () => data({ ...guestShare, itemId: "20000000-0000-4000-8000-0000000000c1", itemTitle: "Some Show", itemKind: "Series" }))
      .on("GET", "/api/v1/items/:id/details", () => data({ genres: [], externalIds: [], nfo: { status: "unread", fields: [] }, ...episode }))
      .on("GET", "/api/v1/items/:id/sources", () => data({ itemId: episode.id, sources: [] }));
    const { wrapper } = await mountView(`/items/${episode.id}`, { fetch: server.fetch, user: guestUser });
    await vi.waitFor(() => {
      expect(wrapper.find("[data-testid='guest-crumbs']").text()).toContain("Some Show");
    });
    const crumbs = wrapper.find("[data-testid='guest-crumbs']");
    expect(crumbs.find("a[href='/shared']").text()).toBe("Some Show");
    expect(crumbs.find(`a[href='/items/${episode.parentId}']`).exists()).toBe(true);
    expect(wrapper.find("a[href='/libraries']").exists()).toBe(false);
    expect(server.calls("GET", "/api/v1/libraries")).toHaveLength(0);
    // Only routes the server allows guests were called (its guest allow list).
    const allowed =
      /^\/api\/v1\/(users\/me|users\/me\/preferences|users\/me\/resume|auth\/csrf|shares\/current|site\/appearance|site\/plugins|items|items\/[^/]+(\/details|\/sources|\/user-data)?)$/;
    expect(server.requests.map((request) => request.path).filter((path) => !allowed.test(path))).toEqual([]);
    expectNoPlaybackMarkup(wrapper.html());
  });
});

describe("share links: guest navigation", () => {
  async function boot(user: User) {
    const server = guestRoutes(createRouteFetch())
      .on("GET", "/api/v1/users/me", () => data(user))
      .on("GET", "/api/v1/auth/csrf", () => data({ csrf: "csrf-guest" }))
      .on("GET", "/api/v1/users/me/preferences", () => apiError(404, "not_found"))
      .on("GET", "/api/v1/libraries", () => data({ libraries: [], pagination: { limit: 50, nextCursor: "" } }));
    const host = createApp(App);
    const plugins = installAppPlugins(host, { languages: ["en-US"], fetch: server.fetch, history: createMemoryHistory(), plugins: { bundles: [] } });
    await plugins.router.push("/settings");
    await plugins.router.isReady();
    const wrapper = mount(App, {
      global: { plugins: [plugins.router, plugins.i18n, plugins.pinia], provide: host._context.provides },
      attachTo: document.body,
    });
    await flushPromises();
    return { wrapper, router: plugins.router };
  }

  it("hides everything but the share from a guest and keeps it off other pages", async () => {
    const { wrapper, router } = await boot(guestUser);
    try {
      await vi.waitFor(() => {
        expect(router.currentRoute.value.name).toBe("shared");
      });
      const nav = wrapper.find("[data-testid='guest-nav']");
      expect(nav.exists()).toBe(true);
      expect(nav.findAll("a").map((link) => link.text())).toEqual(["Shared with you"]);
      expect(wrapper.find("[data-testid='admin-link']").exists()).toBe(false);
      expect(wrapper.find("a[href='/settings']").exists()).toBe(false);
      expect(wrapper.find("a[href='/account']").exists()).toBe(false);
      expect(wrapper.find("input[type='search']").exists()).toBe(false);
      expect(wrapper.text()).toContain("Guest of a share link");
      expect(wrapper.text()).not.toContain(guestUser.name);
    } finally {
      wrapper.unmount();
    }
  });

  it("keeps the full navigation for a regular account", async () => {
    const { wrapper, router } = await boot(adminUser);
    try {
      await vi.waitFor(() => {
        expect(router.currentRoute.value.name).toBe("settings");
      });
      expect(wrapper.find("[data-testid='guest-nav']").exists()).toBe(false);
      expect(wrapper.find("[data-testid='admin-link']").exists()).toBe(true);
    } finally {
      wrapper.unmount();
    }
  });
});
