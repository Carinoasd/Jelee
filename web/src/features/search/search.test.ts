import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/schema";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, json, regularUser } from "@/test/routeFetch";
import { normalizeQuery, parseSearchKind, parseSearchLibrary, searchGroupSize, searchPageSize } from "./api";
import SearchBox from "./SearchBox.vue";

type CatalogItem = components["schemas"]["CatalogItem"];

afterEach(() => {
  unmountAll();
  vi.useRealTimers();
});

const libraryA = "10000000-0000-4000-8000-00000000000a";
const libraryB = "10000000-0000-4000-8000-00000000000b";

const item = (n: number, title = `Movie ${n}`, kind: CatalogItem["kind"] = "Movie", libraryId = libraryA): CatalogItem => ({
  id: `20000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
  libraryId,
  kind,
  title,
  productionYear: 2000 + n,
});

function catalog(items: CatalogItem[]) {
  return createRouteFetch()
    .on("GET", "/api/v1/items", ({ url }) => {
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      const type = url.searchParams.getAll("type");
      const library = url.searchParams.get("libraryId");
      const matched = items.filter(
        (entry) =>
          entry.title.toLowerCase().includes(q) && (type.length === 0 || type.includes(entry.kind)) && (library === null || entry.libraryId === library),
      );
      const offset = Number(url.searchParams.get("offset") ?? "0");
      const limit = Number(url.searchParams.get("limit") ?? "50");
      return json(200, { data: matched.slice(offset, offset + limit), pagination: { nextCursor: "", limit, offset, total: matched.length } });
    })
    .on("GET", "/api/v1/libraries", () =>
      data({
        libraries: [
          { id: libraryA, name: "Films", roots: 1 },
          { id: libraryB, name: "Shows", roots: 1 },
        ],
        pagination: { limit: 50, nextCursor: "" },
      }),
    );
}

const itemCalls = (server: ReturnType<typeof catalog>) => server.calls("GET", "/api/v1/items").map((request) => request.url.searchParams);
const links = (wrapper: Awaited<ReturnType<typeof mountView>>["wrapper"]) => wrapper.findAll("a[href^='/items/']");

describe("search helpers", () => {
  it("normalizes queries, kinds and libraries", () => {
    expect(normalizeQuery("  arrival  ")).toBe("arrival");
    expect(normalizeQuery(["x"])).toBe("");
    expect(Array.from(normalizeQuery("字".repeat(200)))).toHaveLength(128);
    expect(parseSearchKind("Series")).toBe("Series");
    expect(parseSearchKind("Season")).toBeNull();
    expect(parseSearchLibrary(libraryA.toUpperCase())).toBe(libraryA);
    expect(parseSearchLibrary("../x")).toBeNull();
  });
});

describe("search view", () => {
  it("asks for a query without searching", async () => {
    const server = catalog([item(1)]);
    const { wrapper } = await mountView("/search", { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("h1").text()).toBe("Search");
    expect(wrapper.text()).toContain("Type a title in the search box");
    expect(itemCalls(server)).toHaveLength(0);
  });

  it("groups the results by kind with each kind's total", async () => {
    const items = [
      ...Array.from({ length: searchGroupSize + 3 }, (_, n) => item(n + 1)),
      item(100, "Movie night", "Series"),
      item(101, "Movie night 1x01", "Episode"),
    ];
    const server = catalog(items);
    const { wrapper } = await mountView("/search?q=movie", { fetch: server.fetch, user: regularUser });
    const calls = itemCalls(server);
    expect(calls.map((params) => params.getAll("type"))).toEqual([["Movie"], ["Series"], ["Episode"], ["HomeVideo"]]);
    expect(calls.every((params) => params.get("q") === "movie" && params.get("limit") === String(searchGroupSize) && params.get("sort") === "name")).toBe(true);
    expect(wrapper.find("h1").text()).toBe("Results for “movie”");
    expect(wrapper.find("[role=status]").text()).toBe(`${searchGroupSize + 5} results`);
    const groups = wrapper.findAll("section section");
    expect(groups.map((group) => group.find("h2").text())).toEqual([`Movie${searchGroupSize + 3}`, "Series1", "Episode1"]);
    expect(groups[0]!.findAll("a[href^='/items/']")).toHaveLength(searchGroupSize);
    expect(groups[0]!.find("h2 a").attributes("href")).toBe("/search?q=movie&type=Movie");
    // The group names the kind, so the cards show only the year.
    expect(groups[1]!.find(".jl-card__kind").text()).toBe("2100");
    expect(groups[0]!.find("img").attributes("srcset")).toContain("width=240 240w");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("lists every result of one kind and loads more", async () => {
    const items = Array.from({ length: searchPageSize + 5 }, (_, n) => item(n + 1));
    const server = catalog(items);
    const { wrapper } = await mountView("/search?q=movie&type=Movie", { fetch: server.fetch, user: regularUser });
    const first = itemCalls(server)[0]!;
    expect(first.get("offset")).toBe("0");
    expect(first.get("limit")).toBe(String(searchPageSize));
    expect(first.getAll("type")).toEqual(["Movie"]);
    expect(wrapper.find("[role=status]").text()).toBe(`Showing ${searchPageSize} of ${searchPageSize + 5}`);

    await wrapper.findAll("button").find((button) => button.text() === "Load more")!.trigger("click");
    await flushPromises();
    expect(itemCalls(server)[1]!.get("offset")).toBe(String(searchPageSize));
    expect(links(wrapper)).toHaveLength(searchPageSize + 5);
    expect(wrapper.findAll("button").some((button) => button.text() === "Load more")).toBe(false);
  });

  it("narrows by kind and library through the URL and reuses recent answers", async () => {
    const server = catalog([item(1), item(2, "Movie show", "Series", libraryB), item(3, "Movie two", "Movie", libraryB)]);
    const { wrapper, router } = await mountView("/search?q=movie", { fetch: server.fetch, user: regularUser });
    const kinds = () => wrapper.findAll("[role=group] button");
    expect(kinds().map((button) => button.text())).toEqual(["All types", "Movie", "Series", "Episode", "Home video"]);
    expect(kinds()[0]!.attributes("aria-pressed")).toBe("true");
    await kinds()[2]!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ q: "movie", type: "Series" });
    expect(links(wrapper).map((a) => a.find(".jl-card__title").text())).toEqual(["Movie show"]);

    const select = wrapper.find("select");
    expect(select.findAll("option").map((option) => option.text())).toEqual(["All libraries", "Films", "Shows"]);
    await kinds()[0]!.trigger("click");
    await flushPromises();
    await select.setValue(libraryB);
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ q: "movie", library: libraryB });
    expect(itemCalls(server).at(-1)!.get("libraryId")).toBe(libraryB);
    expect(links(wrapper).map((a) => a.find(".jl-card__title").text())).toEqual(["Movie two", "Movie show"]);

    // Back to every library: the overview answered a moment ago is reused.
    const before = itemCalls(server).length;
    await select.setValue("");
    await flushPromises();
    expect(itemCalls(server)).toHaveLength(before);
    expect(links(wrapper)).toHaveLength(3);
  });

  it("moves between results with the arrow keys", async () => {
    const server = catalog([item(1), item(2), item(3)]);
    const { wrapper } = await mountView("/search?q=movie&type=Movie", { fetch: server.fetch, user: regularUser });
    const anchors = links(wrapper);
    (anchors[0]!.element as HTMLElement).focus();
    await anchors[0]!.trigger("keydown", { key: "ArrowRight" });
    expect(document.activeElement).toBe(anchors[1]!.element);
    await anchors[1]!.trigger("keydown", { key: "End" });
    expect(document.activeElement).toBe(anchors[2]!.element);
    await anchors[2]!.trigger("keydown", { key: "ArrowLeft" });
    expect(document.activeElement).toBe(anchors[1]!.element);
  });

  it("shows the empty state with the query", async () => {
    const server = catalog([item(1)]);
    const { wrapper } = await mountView("/search?q=nothing", { fetch: server.fetch, user: regularUser });
    expect(wrapper.text()).toContain("Nothing matches “nothing”");
  });

  it("shows a localized error with trace ID and retries without the cache", async () => {
    const server = catalog([]).on("GET", "/api/v1/items", () => apiError(503, "not_ready"));
    const { wrapper } = await mountView("/search?q=x&type=Movie", { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("[role=alert]").text()).toContain("The server is still starting");
    expect(wrapper.text()).toContain("trace-not_ready");
    expect(wrapper.text()).not.toContain("raw server text");
    server.on("GET", "/api/v1/items", ({ url }) =>
      json(200, { data: [item(1, "X-Men")], pagination: { nextCursor: "", limit: 40, offset: Number(url.searchParams.get("offset")), total: 1 } }),
    );
    await wrapper.findAll("button").find((button) => button.text() === "Retry")!.trigger("click");
    await flushPromises();
    expect(itemCalls(server)).toHaveLength(2);
    expect(wrapper.find(".jl-card__title").text()).toBe("X-Men");
  });
});

describe("header search box", () => {
  it("opens the search page with Enter from another page", async () => {
    const server = catalog([item(1)]);
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
    expect(new Set(itemCalls(server).map((params) => params.get("q")))).toEqual(new Set(["movie"]));
  });

  it("debounces typing on the search page and moves into the results with ArrowDown", async () => {
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
    expect([...new Set(itemCalls(server).map((params) => params.get("q")))]).toEqual(["ar", "arriv"]);
    expect(links(wrapper).map((a) => a.find(".jl-card__title").text())).toEqual(["Arrival"]);

    await input.trigger("keydown", { key: "ArrowDown" });
    expect(document.activeElement).toBe(links(wrapper)[0]!.element);

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
