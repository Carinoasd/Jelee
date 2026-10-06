import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { button, control, toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup } from "@/test/routeFetch";
import { ratingProblems, ratingRow, ratingsBody } from "./ratings";

afterEach(() => {
  unmountAll();
});

const ratings = [
  { code: "G", level: 0 },
  { code: "PG-13", level: 13 },
  { code: "TV-14", level: 14 },
  { code: "R", level: 17 },
  { code: "TV-MA", level: 17 },
];

function policyServer() {
  return createRouteFetch()
    .on("GET", "/api/v1/access/policy", () => data({ blockUnrated: false, restrictAdmins: false }))
    .on("GET", "/api/v1/access/parental-ratings", () => data(ratings))
    .on("PUT", "/api/v1/access/policy", ({ body }) => data(body));
}

describe("content access policy view", () => {
  it("shows the policy, the precedence and the rating table", async () => {
    const server = policyServer();
    const { wrapper } = await mountView("/admin/access", { fetch: server.fetch, user: adminUser });
    expect(wrapper.find("h1").text()).toBe("Content access");
    expect(control(wrapper, "Hide unrated items under a rating ceiling").element.checked).toBe(false);
    expect(wrapper.findAll("ol li")).toHaveLength(5);
    expect(wrapper.find("a[href='/admin/users']").exists()).toBe(true);
    const rows = wrapper.findAll("tbody tr");
    expect(rows).toHaveLength(4);
    expect(rows[3]!.find("th[scope=row]").text()).toBe("Age 17");
    expect(rows[3]!.find("td").text()).toBe("R, TV-MA");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("saves only after the second press", async () => {
    const server = policyServer();
    const { wrapper } = await mountView("/admin/access", { fetch: server.fetch, user: adminUser });
    expect(button(wrapper, "Save").element.disabled).toBe(true);
    await control(wrapper, "Apply restrictions to administrators too").setValue(true);
    await button(wrapper, "Save").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/access/policy")).toHaveLength(0);
    expect(wrapper.text()).toContain("apply to every account's next request");
    await button(wrapper, "Apply to all accounts").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/access/policy")[0]!.body).toEqual({ blockUnrated: false, restrictAdmins: true });
    expect(toastKeys(wrapper)).toContain("access.policy.saved");
  });

  it("shows localized errors with trace IDs, retries and the empty rating table", async () => {
    let fail = true;
    const server = policyServer()
      .on("GET", "/api/v1/access/policy", () => (fail ? apiError(503, "not_ready") : data({ blockUnrated: true, restrictAdmins: false })))
      .on("GET", "/api/v1/access/parental-ratings", () => data([]));
    const { wrapper } = await mountView("/admin/access", { fetch: server.fetch, user: adminUser });
    expect(wrapper.text()).toContain("The server is still starting");
    expect(wrapper.text()).toContain("trace-not_ready");
    expect(wrapper.text()).not.toContain("raw server text");
    expect(wrapper.text()).toContain("No rating codes are defined");
    fail = false;
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    expect(control(wrapper, "Hide unrated items under a rating ceiling").element.checked).toBe(true);
  });

  it("shows skeletons while loading", async () => {
    const server = createRouteFetch()
      .on("GET", "/api/v1/access/policy", () => new Promise<Response>(() => undefined))
      .on("GET", "/api/v1/access/parental-ratings", () => new Promise<Response>(() => undefined));
    const { wrapper } = await mountView("/admin/access", { fetch: server.fetch, user: adminUser });
    expect(wrapper.findAll("[aria-busy=true]")).toHaveLength(2);
    expect(wrapper.find(".jl-skeleton").exists()).toBe(true);
  });

  it("checks rating codes as the server would store them", () => {
    const rows = [ratingRow(" pg-13 ", 13), ratingRow("PG-13", 12), ratingRow("Rated R", 17), ratingRow("us:pg", 7), ratingRow("", 0), ratingRow("TV-Y", 0)];
    expect([...ratingProblems(rows).values()]).toEqual([
      "contentRules.ratings.codeDuplicate",
      "contentRules.ratings.codePrefix",
      "contentRules.ratings.codePrefix",
      "contentRules.ratings.codeEmpty",
    ]);
    expect(ratingsBody([rows[0]!, rows[5]!])).toEqual([
      { code: "PG-13", level: 13 },
      { code: "TV-Y", level: 0 },
    ]);
  });

  it("replaces the rating table after the confirmation", async () => {
    const server = policyServer().on("PUT", "/api/v1/access/parental-ratings", ({ body }) => data((body as { ratings: unknown[] }).ratings));
    const { wrapper } = await mountView("/admin/access", { fetch: server.fetch, user: adminUser });
    await button(wrapper, "Edit codes").trigger("click");
    await flushPromises();
    const code = (n: number) => wrapper.find<HTMLInputElement>(`input[aria-label="Code ${n}"]`);
    expect(code(1).element.value).toBe("G");
    expect(code(5).element.value).toBe("TV-MA");
    await wrapper.find("[aria-label='Remove code 5']").trigger("click");
    await button(wrapper, "Add code").trigger("click");
    await flushPromises();
    await code(5).setValue("us:nc-17");
    expect(wrapper.text()).toContain('Leave out the "Rated " or country prefix.');
    expect(button(wrapper, "Save codes").element.disabled).toBe(true);
    await code(5).setValue(" nc-17 ");
    await wrapper.find<HTMLSelectElement>("select[aria-label='Level of code 5']").setValue("18");
    await button(wrapper, "Save codes").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/access/parental-ratings")).toHaveLength(0);
    expect(wrapper.text()).toContain("The whole table is replaced.");
    server.on("GET", "/api/v1/access/parental-ratings", () => data([...ratings.slice(0, 4), { code: "NC-17", level: 18 }]));
    await button(wrapper, "Replace the table").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/access/parental-ratings")[0]!.body).toEqual({
      ratings: [...ratings.slice(0, 4), { code: "NC-17", level: 18 }],
    });
    expect(toastKeys(wrapper)).toContain("contentRules.ratings.saved");
    expect(wrapper.find("input[aria-label='Code 1']").exists()).toBe(false);
    const rows = wrapper.findAll("section[aria-labelledby='access-ratings-title'] tbody tr");
    expect(rows.at(-1)!.text()).toContain("NC-17");
  });
});
