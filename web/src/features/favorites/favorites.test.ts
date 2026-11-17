import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import enCollections from "@/i18n/en-US/collections.json";
import enCommon from "@/i18n/en-US/common.json";
import { toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, regularUser, type RouteFetch } from "@/test/routeFetch";
import type { FavoriteItem } from "./api";

const text = enCollections.collections.favorites;
const libraryId = "10000000-0000-4000-8000-00000000000a";
const itemId = "20000000-0000-4000-8000-000000000001";

function favorite(n: number): FavoriteItem {
  return { id: "20000000-0000-4000-8000-" + String(n).padStart(12, "0"), libraryId, kind: n % 2 === 0 ? "Series" : "Movie", title: `Title ${n}`, favoritedAt: "2026-10-31T00:00:00Z" };
}

function details(isFavorite: boolean) {
  return { id: itemId, libraryId, kind: "Movie", title: "Heat", genres: [], externalIds: [], nfo: { status: "unread", fields: [] }, isFavorite };
}

function itemRoutes(isFavorite: boolean): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/libraries", () => data({ libraries: [], pagination: { nextCursor: "", limit: 50 } }))
    .on("GET", "/api/v1/items/:id/details", () => data(details(isFavorite)))
    .on("GET", "/api/v1/items/:id/sources", () => data({ itemId, sources: [] }))
    .on("PUT", "/api/v1/items/:id/favorite", () => data({ itemId, resumeTicks: 0, played: false, playCount: 0, isFavorite: true }))
    .on("DELETE", "/api/v1/items/:id/favorite", () => data({ itemId, resumeTicks: 0, played: false, playCount: 0, isFavorite: false }));
}

async function openItem(fetch: RouteFetch): Promise<VueWrapper> {
  const { wrapper } = await mountView("/items/" + itemId, { fetch: fetch.fetch, user: regularUser });
  for (let i = 0; i < 3; i++) {
    await flushPromises();
    await vi.dynamicImportSettled();
  }
  return wrapper;
}

afterEach(() => {
  unmountAll();
});

describe("favorite toggle", () => {
  it("marks and unmarks the item from its page", async () => {
    const server = itemRoutes(false);
    const wrapper = await openItem(server);
    const toggle = wrapper.find("[data-testid='favorite-toggle']");
    expect(toggle.attributes("aria-pressed")).toBe("false");
    await toggle.trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `/api/v1/items/${itemId}/favorite`)).toHaveLength(1);
    expect(server.calls("PUT", `/api/v1/items/${itemId}/favorite`)[0]!.body).toEqual({});
    expect(wrapper.find("[data-testid='favorite-toggle']").attributes("aria-pressed")).toBe("true");
    await wrapper.find("[data-testid='favorite-toggle']").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/items/${itemId}/favorite`)).toHaveLength(1);
    expect(wrapper.find("[data-testid='favorite-toggle']").attributes("aria-pressed")).toBe("false");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("starts pressed for a favourite and keeps the state when the server refuses", async () => {
    const server = itemRoutes(true).on("DELETE", "/api/v1/items/:id/favorite", () => apiError(404, "not_found"));
    const wrapper = await openItem(server);
    const toggle = wrapper.find("[data-testid='favorite-toggle']");
    expect(toggle.attributes("aria-pressed")).toBe("true");
    await toggle.trigger("click");
    await flushPromises();
    expect(wrapper.find("[data-testid='favorite-toggle']").attributes("aria-pressed")).toBe("true");
    expect(toastKeys(wrapper)).toContain("errors.notFound");
  });
});

describe("favorites page", () => {
  it("lists the favourites and pages with the cursor", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/users/me/favorites", ({ url }) =>
      url.searchParams.get("cursor") === "o.60"
        ? data({ items: [favorite(61)], total: 61, offset: 60, limit: 60, nextCursor: "" })
        : data({ items: Array.from({ length: 60 }, (_, n) => favorite(n + 1)), total: 61, offset: 0, limit: 60, nextCursor: "o.60" }),
    );
    const { wrapper } = await mountView("/favorites", { fetch: server.fetch, user: regularUser });
    await flushPromises();
    expect(wrapper.find("h1").text()).toBe(text.title);
    expect(wrapper.findAll("[data-testid='favorites'] li")).toHaveLength(60);
    const more = wrapper.findAll("button").find((b) => b.text() === enCommon.common.loadMore);
    await more!.trigger("click");
    await flushPromises();
    expect(wrapper.findAll("[data-testid='favorites'] li")).toHaveLength(61);
    expect(wrapper.text()).toContain("Title 61");
    expect(wrapper.findAll("button").some((b) => b.text() === enCommon.common.loadMore)).toBe(false);
    const queries = server.calls("GET", "/api/v1/users/me/favorites").map((call) => Object.fromEntries(new URL(call.url).searchParams));
    expect(queries).toEqual([{ limit: "60" }, { limit: "60", cursor: "o.60" }]);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows an empty state", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/users/me/favorites", () => data({ items: [], total: 0, offset: 0, limit: 60, nextCursor: "" }));
    const { wrapper } = await mountView("/favorites", { fetch: server.fetch, user: regularUser });
    await flushPromises();
    expect(wrapper.text()).toContain(text.empty);
  });

  it("is linked from the home page", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/items", () => data([]));
    const { wrapper } = await mountView("/", { fetch: server.fetch, user: regularUser });
    await flushPromises();
    expect(wrapper.find("a[href='/favorites']").text()).toBe(enCommon.common.favorites);
  });
});
