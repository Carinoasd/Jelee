import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/schema";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, json, regularUser } from "@/test/routeFetch";
import { normalizeQuery, parseSearchKind, searchPageSize } from "./api";
import SearchBox from "./SearchBox.vue";

type CatalogItem = components["schemas"]["CatalogItem"];

afterEach(() => {
  unmountAll();
  vi.useRealTimers();
});

const item = (n: number, title = `Movie ${n}`): CatalogItem => ({
  id: `20000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
  libraryId: "10000000-0000-4000-8000-00000000000a",
  kind: "Movie",
  title,
  productionYear: 2000 + n,
});

function catalog(items: CatalogItem[]) {
  return createRouteFetch().on("GET", "/api/v1/items", ({ url }) => {
    const q = (url.searchParams.get("q") ?? "").toLowerCase();
    const type = url.searchParams.getAll("type");
    const matched = items.filter((entry) => entry.title.toLowerCase().includes(q) && (type.length === 0 || type.includes(entry.kind)));
    const offset = Number(url.searchParams.get("offset") ?? "0");
    const limit = Number(url.searchParams.get("limit") ?? "50");
    return json(200, { data: matched.slice(offset, offset + limit), pagination: { nextCursor: "", limit, offset, total: matched.length } });
  });
}

describe("search helpers", () => {
  it("normalizes queries and kinds", () => {
    expect(normalizeQuery("  arrival  ")).toBe("arrival");
    expect(normalizeQuery(["x"])).toBe("");
    expect(Array.from(normalizeQuery("字".repeat(200)))).toHaveLength(128);
    expect(parseSearchKind("Series")).toBe("Series");
    expect(parseSearchKind("Season")).toBeNull();
  });
});

describe("search view", () => {
  it("asks for a query without calling the server", async () => {
    const server = catalog([item(1)]);
    const { wrapper } = await mountView("/search", { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("h1").text()).toBe("Search");
    expect(wrapper.text()).toContain("Type a title in the search box");
    expect(server.requests).toHaveLength(0);
  });

  it("searches on the server, lists results and loads more", async () => {
    const items = Array.from({ length: searchPageSize + 5 }, (_, n) => item(n + 1));
    const server = catalog(items);
    const { wrapper } = await mountView("/search?q=movie", { fetch: server.fetch, user: regularUser });
    const first = server.calls("GET", "/api/v1/items")[0]!.url.searchParams;
    expect(first.get("q")).toBe("movie");
    expect(first.get("offset")).toBe("0");
    expect(first.get("limit")).toBe(String(searchPageSize));
    expect(first.get("sort")).toBe("name");
    expect(wrapper.find("h1").text()).toBe("Results for “movie”");
    expect(wrapper.find("[role=status]").text()).toBe(`Showing ${searchPageSize} of ${searchPageSize + 5}`);
    const link = wrapper.find("a[href^='/items/']");
    expect(link.text()).toBe("Movie 1");
    expect(wrapper.text()).toContain("2001");

    await wrapper.findAll("button").find((button) => button.text() === "Load more")!.trigger("click");
    await flushPromises();
    expect(server.calls("GET", "/api/v1/items")[1]!.url.searchParams.get("offset")).toBe(String(searchPageSize));
    expect(wrapper.findAll("a[href^='/items/']")).toHaveLength(searchPageSize + 5);
    expect(wrapper.findAll("button").some((button) => button.text() === "Load more")).toBe(false);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("narrows by type through the URL", async () => {
    const server = catalog([item(1), { ...item(2, "Movie show"), kind: "Series" }]);
    const { wrapper, router } = await mountView("/search?q=movie", { fetch: server.fetch, user: regularUser });
    await wrapper.find("select").setValue("Series");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ q: "movie", type: "Series" });
    expect(server.calls("GET", "/api/v1/items").at(-1)!.url.searchParams.getAll("type")).toEqual(["Series"]);
    expect(wrapper.findAll("a[href^='/items/']").map((a) => a.text())).toEqual(["Movie show"]);
  });

  it("shows the empty state with the query", async () => {
    const server = catalog([item(1)]);
    const { wrapper } = await mountView("/search?q=nothing", { fetch: server.fetch, user: regularUser });
    expect(wrapper.text()).toContain("Nothing matches “nothing”");
  });

  it("shows a localized error with trace ID and retries", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/items", () => apiError(503, "not_ready"));
    const { wrapper } = await mountView("/search?q=x", { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("[role=alert]").text()).toContain("The server is still starting");
    expect(wrapper.text()).toContain("trace-not_ready");
    expect(wrapper.text()).not.toContain("raw server text");
    server.on("GET", "/api/v1/items", ({ url }) =>
      json(200, { data: [item(1, "X-Men")], pagination: { nextCursor: "", limit: 40, offset: Number(url.searchParams.get("offset")), total: 1 } }),
    );
    await wrapper.findAll("button").find((button) => button.text() === "Retry")!.trigger("click");
    await flushPromises();
    expect(server.calls("GET", "/api/v1/items")).toHaveLength(2);
    expect(wrapper.find("a[href^='/items/']").text()).toBe("X-Men");
  });
});

describe("header search box", () => {
  it("opens the search page with Enter from another page", async () => {
    const server = catalog([item(1)]).on("GET", "/api/v1/libraries", () =>
      data({ libraries: [], pagination: { limit: 50, nextCursor: "" } }),
    );
    const { wrapper, router } = await mountView("/libraries", { fetch: server.fetch, user: regularUser, header: SearchBox });
    const input = wrapper.find("input[type=search]");
    expect(input.attributes("aria-keyshortcuts")).toBe("/");
    expect(wrapper.find("form[role=search] label").text()).toBe("Search the libraries");
    await input.setValue("  movie ");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("libraries");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(router.currentRoute.value.fullPath).toBe("/search?q=movie");
    expect(server.calls("GET", "/api/v1/items")).toHaveLength(1);
  });

  it("debounces typing on the search page into one request", async () => {
    vi.useFakeTimers();
    const server = catalog([item(1, "Arrival"), item(2, "Arrow")]);
    const { wrapper, router } = await mountView("/search?q=ar", { fetch: server.fetch, user: regularUser, header: SearchBox });
    const input = wrapper.find("input[type=search]");
    expect((input.element as HTMLInputElement).value).toBe("ar");
    await input.setValue("arr");
    await input.setValue("arri");
    await input.setValue("arriv");
    await vi.advanceTimersByTimeAsync(299);
    expect(router.currentRoute.value.query.q).toBe("ar");
    await vi.advanceTimersByTimeAsync(1);
    await flushPromises();
    expect(router.currentRoute.value.query.q).toBe("arriv");
    const queries = server.calls("GET", "/api/v1/items").map((request) => request.url.searchParams.get("q"));
    expect(queries).toEqual(["ar", "arriv"]);
    expect(wrapper.findAll("a[href^='/items/']").map((a) => a.text())).toEqual(["Arrival"]);

    await input.trigger("keydown", { key: "Escape" });
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
    expect(router.currentRoute.value.query.q).toBeUndefined();
    expect(wrapper.text()).toContain("Type a title in the search box");
  });

  it("focuses with the / key unless another field is being edited", async () => {
    const server = catalog([]);
    const { wrapper } = await mountView("/search", { fetch: server.fetch, user: regularUser, header: SearchBox });
    const input = wrapper.find("input[type=search]").element as HTMLInputElement;
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    expect(document.activeElement).toBe(input);
    input.blur();
    const other = document.createElement("textarea");
    document.body.append(other);
    other.focus();
    other.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    expect(document.activeElement).toBe(other);
    other.remove();
  });
});
