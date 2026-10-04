import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, regularUser } from "@/test/routeFetch";
import { exportUrl, isEmptyReport, rangeFor, type WatchStatsReport } from "./api";

afterEach(unmountAll);

const itemId = "20000000-0000-4000-8000-000000000001";
const libraryId = "10000000-0000-4000-8000-00000000000a";

const figures = { sessions: 3, views: 2, firstPlays: 1, rewatches: 1, completions: 2, completionRate: 0.75, effectiveSeconds: 7200 };

function report(overrides: Partial<WatchStatsReport> = {}): WatchStatsReport {
  return {
    from: "2026-09-05",
    to: "2026-10-04",
    period: "day",
    timeZone: "Asia/Taipei",
    weekStart: "monday",
    totals: figures,
    periods: [
      { ...figures, start: "2026-10-03", effectiveSeconds: 1800 },
      { ...figures, start: "2026-10-04", effectiveSeconds: 5400 },
    ],
    topItems: [
      {
        ...figures,
        itemId,
        libraryId,
        kind: "Movie",
        title: "Arrival <b>x</b>",
        userData: { itemId, played: true, playCount: 1, resumeTicks: 0 },
      },
    ],
    libraries: [{ ...figures, libraryId, name: "Movies" }],
    kinds: [{ ...figures, kind: "Movie" }],
    ...overrides,
  };
}

const button = (wrapper: Awaited<ReturnType<typeof mountView>>["wrapper"], text: string) => {
  const found = wrapper.findAll("button").find((candidate) => candidate.text() === text);
  if (found === undefined) {
    throw new Error("no button " + text);
  }
  return found;
};

describe("statistics helpers", () => {
  it("derives the range from the grouping", () => {
    const today = new Date(2026, 9, 4);
    expect(rangeFor("day", today)).toEqual({ from: "2026-09-05", to: "2026-10-04" });
    expect(rangeFor("week", today)).toEqual({ from: "2026-07-13", to: "2026-10-04" });
    expect(rangeFor("month", today)).toEqual({ from: "2025-10-05", to: "2026-10-04" });
    expect(rangeFor("year", today).from).toBe("2021-10-05");
  });

  it("builds same-origin export links and detects empty reports", () => {
    expect(exportUrl({ from: "2026-01-01", to: "2026-01-31" }, "csv")).toBe("/api/v1/watch-stats/export?from=2026-01-01&to=2026-01-31&format=csv");
    expect(isEmptyReport(report())).toBe(false);
    expect(isEmptyReport(report({ totals: { ...figures, sessions: 0, effectiveSeconds: 0 }, periods: [] }))).toBe(true);
  });
});

describe("my watch statistics", () => {
  it("shows totals, a chart, top items and breakdowns without any delivery URL", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/users/me/watch-stats", () => data(report()));
    const { wrapper } = await mountView("/stats", { fetch: server.fetch, user: regularUser });
    const query = server.calls("GET", "/api/v1/users/me/watch-stats")[0]!.url.searchParams;
    expect(query.get("period")).toBe("day");
    expect(query.get("top")).toBe("10");
    expect(query.get("from")).toBe(rangeFor("day").from);
    expect(query.get("to")).toBe(rangeFor("day").to);

    const text = wrapper.text();
    expect(wrapper.find("h1").text()).toBe("My watch statistics");
    expect(text).toContain("2 hr");
    expect(text).toContain("75%");
    expect(text).toContain("Asia/Taipei");
    expect(wrapper.find("svg[aria-hidden=true]").findAll("rect")).toHaveLength(2);
    expect(wrapper.find("figure table").text()).toContain("1.5 hr");
    const link = wrapper.find(`a[href='/items/${itemId}']`);
    expect(link.text()).toBe("Arrival <b>x</b>");
    expect(wrapper.html()).not.toContain("<b>x</b>");
    expect(text).toContain("Finished");
    expect(wrapper.findAll("caption").map((caption) => caption.text())).toEqual(["By library", "By type"]);

    const html = wrapper.html();
    expectNoPlaybackMarkup(html);
    expect(html).not.toMatch(/\/api\/v1\/sources|\/stream|resumeTicks|playCount/);
    expect(wrapper.findAll("a").every((anchor) => !(anchor.attributes("href") ?? "").startsWith("/api/"))).toBe(true);
  });

  it("reloads with the chosen grouping and top size", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/users/me/watch-stats", ({ url }) =>
      data(report({ period: url.searchParams.get("period") as WatchStatsReport["period"] })),
    );
    const { wrapper } = await mountView("/stats", { fetch: server.fetch, user: regularUser });
    const [period, top] = wrapper.findAll("select");
    await period!.setValue("month");
    await flushPromises();
    await top!.setValue("25");
    await flushPromises();
    const calls = server.calls("GET", "/api/v1/users/me/watch-stats").map((request) => request.url.searchParams);
    expect(calls[1]!.get("period")).toBe("month");
    expect(calls[1]!.get("from")).toBe(rangeFor("month").from);
    expect(calls[2]!.get("top")).toBe("25");
    expect(calls[2]!.get("period")).toBe("month");
  });

  it("shows the empty state and the error state with retry", async () => {
    const empty = report({ totals: { ...figures, sessions: 0, effectiveSeconds: 0 }, periods: [], topItems: [], libraries: [], kinds: [] });
    const server = createRouteFetch().on("GET", "/api/v1/users/me/watch-stats", () => apiError(500, "internal_error"));
    const { wrapper } = await mountView("/stats", { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("[role=alert]").text()).toContain("The server ran into an error");
    expect(wrapper.text()).not.toContain("raw server text");
    server.on("GET", "/api/v1/users/me/watch-stats", () => data(empty));
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("No viewing recorded in this period");
  });

  it("clears the history only after the second confirmation", async () => {
    let cleared = false;
    const server = createRouteFetch()
      .on("GET", "/api/v1/users/me/watch-stats", () => data(cleared ? report({ totals: { ...figures, sessions: 0, effectiveSeconds: 0 }, periods: [], topItems: [] }) : report()))
      .on("DELETE", "/api/v1/users/me/playback-history", () => {
        cleared = true;
        return noContent();
      });
    const { wrapper } = await mountView("/stats", { fetch: server.fetch, user: regularUser });
    await button(wrapper, "Clear my watch history").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", "/api/v1/users/me/playback-history")).toHaveLength(0);
    expect(wrapper.text()).toContain("This cannot be undone.");
    const confirm = button(wrapper, "Yes, clear everything");
    expect(document.activeElement).toBe(confirm.element);

    await button(wrapper, "Cancel").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", "/api/v1/users/me/playback-history")).toHaveLength(0);

    await button(wrapper, "Clear my watch history").trigger("click");
    await flushPromises();
    await button(wrapper, "Yes, clear everything").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", "/api/v1/users/me/playback-history")).toHaveLength(1);
    expect(server.calls("GET", "/api/v1/users/me/watch-stats")).toHaveLength(2);
    expect(wrapper.text()).toContain("No viewing recorded in this period");
  });

  it("backs out of the confirmation with Escape", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/users/me/watch-stats", () => data(report()));
    const { wrapper } = await mountView("/stats", { fetch: server.fetch, user: regularUser });
    await button(wrapper, "Clear my watch history").trigger("click");
    await flushPromises();
    await button(wrapper, "Yes, clear everything").trigger("keydown", { key: "Escape" });
    await flushPromises();
    expect(wrapper.findAll("button").some((candidate) => candidate.text() === "Yes, clear everything")).toBe(false);
    expect(document.activeElement).toBe(button(wrapper, "Clear my watch history").element);
  });
});

describe("statistics of every user", () => {
  it("lists top users and offers export links for the shown range", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/watch-stats", () =>
      data(report({ topUsers: [{ ...figures, userId: regularUser.id, userName: "kid" }] })),
    );
    const { wrapper } = await mountView("/admin/stats", { fetch: server.fetch, user: adminUser });
    expect(wrapper.find("h1").text()).toBe("Watch statistics of all users");
    expect(wrapper.find(`a[href='/admin/users/${regularUser.id}']`).text()).toBe("kid");
    const { from, to } = rangeFor("day");
    const links = wrapper.findAll("a[download]").map((anchor) => anchor.attributes("href"));
    expect(links).toEqual([
      `/api/v1/watch-stats/export?from=${from}&to=${to}&format=csv`,
      `/api/v1/watch-stats/export?from=${from}&to=${to}&format=ndjson`,
    ]);
    await wrapper.findAll("select")[0]!.setValue("year");
    await flushPromises();
    expect(wrapper.find("a[download]").attributes("href")).toContain(`from=${rangeFor("year").from}`);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows the server's refusal as a localized error", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/watch-stats", () => apiError(403, "forbidden"));
    const { wrapper } = await mountView("/admin/stats", { fetch: server.fetch, user: adminUser });
    expect(wrapper.find("[role=alert]").text()).toContain("You do not have permission");
  });
});
