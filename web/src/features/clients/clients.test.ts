import { flushPromises, type DOMWrapper, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "@/api/schema";
import en from "@/i18n/en-US/clients.json";
import errorsEn from "@/i18n/en-US/errors.json";
import { useToastStore } from "@/stores/toasts";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, type RouteFetch } from "@/test/routeFetch";

type ClientRule = components["schemas"]["ClientRule"];
type ClientPolicy = components["schemas"]["ClientPolicy"];
type KnownClient = components["schemas"]["KnownClient"];
type ClientHitStats = components["schemas"]["ClientHitStats"];
type ClientHit = components["schemas"]["ClientHit"];

const text = en.clients;
const base = "/api/v1/client-control";

const policy: ClientPolicy = {
  unknownClients: "allow",
  exemptAdmins: true,
  exemptLoopback: true,
  updatedAt: "2026-10-01T00:00:00Z",
  version: 3,
};

const observeRule: ClientRule = {
  id: "00000000-0000-4000-8000-0000000000c1",
  dimension: "user_agent",
  match: "prefix",
  pattern: "BadBot/",
  caseFold: false,
  priority: 10,
  action: "observe",
  intent: "deny",
  scopeKind: "global",
  scopeValues: [],
  enabled: true,
  note: "",
  hitCount: 42,
  lastHitAt: "2026-10-03T10:00:00Z",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};

const knownClient: KnownClient = {
  id: "00000000-0000-4000-8000-0000000000d1",
  activeSessions: 3,
  appName: "Infuse",
  appVersion: "8.0",
  deviceName: "Living room",
  deviceId: "dev-1",
  userAgent: "Infuse/8.0",
  lastIp: "198.51.100.7",
  firstSeenAt: "2026-09-01T00:00:00Z",
  lastSeenAt: "2026-10-03T00:00:00Z",
  trusted: false,
  clientKind: "native",
};

const stats: ClientHitStats = {
  since: "2026-10-03T00:00:00Z",
  total: 50,
  blocked: 8,
  observed: 42,
  byAction: [{ value: "deny", hits: 50 }],
  topUserAgents: [{ value: "BadBot/1.0", hits: 42 }],
  topIps: [{ value: "203.0.113.0/24", hits: 50 }],
  topRules: [{ value: observeRule.id, hits: 42 }],
};

const hit: ClientHit = {
  id: 1,
  action: "deny",
  bucket: "2026-10-03T10:00:00Z",
  hits: 5,
  mode: "observe",
  ruleId: observeRule.id,
  surface: "native",
  userAgent: "BadBot/1.0",
  network: "203.0.113.0/24",
};

function button(wrapper: VueWrapper, label: string): DOMWrapper<HTMLButtonElement> {
  const found = wrapper.findAll("button").filter((candidate) => candidate.text() === label);
  if (found.length === 0) {
    throw new Error("no button " + label);
  }
  return found[0] as DOMWrapper<HTMLButtonElement>;
}

function field(wrapper: VueWrapper, label: string): DOMWrapper<HTMLInputElement | HTMLSelectElement> {
  const element = wrapper.findAll("label").find((candidate) => candidate.text() === label);
  const id = element?.attributes("for");
  if (id === undefined) {
    throw new Error("no field " + label);
  }
  return wrapper.find(`[id="${id}"]`);
}

function routes(): RouteFetch {
  return createRouteFetch()
    .on("GET", base + "/policy", () => data(policy))
    .on("GET", base + "/rules", () => data([observeRule]))
    .on("GET", base + "/clients", () => data({ clients: [knownClient], pagination: { limit: 50, nextCursor: "" } }))
    .on("GET", base + "/stats", () => data(stats))
    .on("GET", base + "/hits", () => data({ hits: [hit], pagination: { limit: 50, nextCursor: "" } }));
}

async function open(fetch: RouteFetch, tab = "") {
  const path = tab === "" ? "/admin/clients" : `/admin/clients?tab=${tab}`;
  return mountView(path, { fetch: fetch.fetch, user: adminUser });
}

afterEach(() => {
  unmountAll();
});

describe("client control: policy", () => {
  it("loads the policy and saves a change only after the second press", async () => {
    const server = routes().on("PUT", base + "/policy", ({ body }) => data({ ...policy, ...(body as object), version: 4 }));
    const { wrapper } = await open(server);
    expect(wrapper.find("h1").text()).toBe(text.title);
    expect(wrapper.findAll("h1")).toHaveLength(1);
    expect(wrapper.find('[role="tab"][aria-selected="true"]').text()).toBe(text.tabs.policy);
    expectNoPlaybackMarkup(wrapper.html());

    await field(wrapper, text.policy.unknownClients).setValue("deny");
    await field(wrapper, text.policy.exemptAdmins).setValue(false);
    await button(wrapper, text.policy.save).trigger("click");
    await flushPromises();
    expect(server.calls("PUT", base + "/policy")).toHaveLength(0);
    // The prompt explains the impact, the lockout risk and the recovery command.
    const prompt = wrapper.find('[role="alert"]').text();
    expect(prompt).toContain("unknown client is refused");
    expect(prompt).toContain("lock you out");
    expect(prompt).toContain("jelee-cli access reset-policies");

    await button(wrapper, text.policy.saveConfirm).trigger("click");
    await flushPromises();
    expect(server.calls("PUT", base + "/policy").map((request) => request.body)).toEqual([
      { unknownClients: "deny", exemptAdmins: false, exemptLoopback: true },
    ]);
    expect(useToastStore().toasts.map((toast) => toast.key)).toContain("clients.policy.saved");
  });

  it("shows a localized error with the trace ID and retries", async () => {
    let fail = true;
    const server = routes().on("GET", base + "/policy", () => (fail ? apiError(500, "internal_error") : data(policy)));
    const { wrapper } = await open(server);
    expect(wrapper.text()).toContain(errorsEn.errors.server);
    expect(wrapper.text()).toContain("trace-internal_error");
    expect(wrapper.text()).not.toContain("raw server text");
    fail = false;
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    expect(server.calls("GET", base + "/policy")).toHaveLength(2);
    expect(wrapper.text()).toContain(text.policy.exemptAdmins);
  });

  it("moves between tabs with the arrow keys", async () => {
    const { wrapper, router } = await open(routes());
    await wrapper.find('[role="tab"][aria-selected="true"]').trigger("keydown", { key: "ArrowRight" });
    await flushPromises();
    expect(router.currentRoute.value.query.tab).toBe("rules");
    const selected = wrapper.find('[role="tab"][aria-selected="true"]');
    expect(selected.text()).toBe(text.tabs.rules);
    expect(selected.attributes("tabindex")).toBe("0");
    expect(document.activeElement?.id).toBe("clients-tab-rules");
    expect(wrapper.find('[role="tabpanel"]').attributes("aria-labelledby")).toBe("clients-tab-rules");
    await wrapper.find('[role="tab"][aria-selected="true"]').trigger("keydown", { key: "End" });
    await flushPromises();
    expect(router.currentRoute.value.query.tab).toBe("hits");
  });
});

describe("client control: rules", () => {
  it("lists rules with their hits and shows an empty state", async () => {
    const { wrapper } = await open(routes(), "rules");
    const row = wrapper.find("tbody tr");
    expect(row.text()).toContain("BadBot/");
    expect(row.text()).toContain("42");
    expect(row.text()).toContain("Blocks with: Deny");
    expect(wrapper.findAll("th[scope=col]").length).toBeGreaterThan(0);
    expectNoPlaybackMarkup(wrapper.html());
    unmountAll();

    const empty = await open(routes().on("GET", base + "/rules", () => data([])), "rules");
    expect(empty.wrapper.text()).toContain(text.rules.empty);
  });

  it("starts blocking only after the confirmation, which names the hit count", async () => {
    const server = routes().on("POST", base + "/rules/:id/enforce", () => data({ ...observeRule, action: "deny", intent: undefined }));
    const { wrapper } = await open(server, "rules");
    await button(wrapper, text.rules.enforce).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/rules/${observeRule.id}/enforce`)).toHaveLength(0);
    const prompt = wrapper.find('[role="alert"]').text();
    expect(prompt).toContain("42 hits");
    expect(prompt).toContain("observe mode first");

    await button(wrapper, text.rules.enforceConfirm).trigger("click");
    await flushPromises();
    const calls = server.calls("POST", `${base}/rules/${observeRule.id}/enforce`);
    expect(calls).toHaveLength(1);
    expect(calls[0]?.body).toEqual({});
    expect(button(wrapper, text.rules.observe).exists()).toBe(true);
  });

  it("returns to observing with a single press", async () => {
    const enforcing = { ...observeRule, action: "deny" as const, intent: undefined };
    const server = routes()
      .on("GET", base + "/rules", () => data([enforcing]))
      .on("POST", base + "/rules/:id/observe", () => data(observeRule));
    const { wrapper } = await open(server, "rules");
    await button(wrapper, text.rules.observe).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/rules/${observeRule.id}/observe`)).toHaveLength(1);
  });

  it("deletes a rule only after the confirmation", async () => {
    const server = routes().on("DELETE", base + "/rules/:id", () => noContent());
    const { wrapper } = await open(server, "rules");
    await button(wrapper, "Delete").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `${base}/rules/${observeRule.id}`)).toHaveLength(0);
    await button(wrapper, text.rules.deleteConfirm).trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `${base}/rules/${observeRule.id}`)).toHaveLength(1);
  });

  it("creates a rule with the expected body", async () => {
    const server = routes().on("POST", base + "/rules", ({ body }) => data({ ...observeRule, ...(body as object), id: "new" }, 201));
    const { wrapper } = await open(server, "rules");
    await button(wrapper, text.rules.add).trigger("click");
    await field(wrapper, text.ruleForm.dimension).setValue("header");
    await field(wrapper, text.ruleForm.header).setValue("X-Client");
    await field(wrapper, text.ruleForm.match).setValue("glob");
    await field(wrapper, text.ruleForm.pattern).setValue("evil*");
    await field(wrapper, text.ruleForm.intent).setValue("rate_limit");
    await field(wrapper, text.ruleForm.rateRequests).setValue("30");
    await field(wrapper, text.ruleForm.ratePeriod).setValue("10");
    await field(wrapper, text.ruleForm.priority).setValue("5");
    await field(wrapper, text.ruleForm.note).setValue("scraper");
    await field(wrapper, text.ruleForm.caseFold).setValue(true);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", base + "/rules").map((request) => request.body)).toEqual([
      {
        dimension: "header",
        header: "X-Client",
        match: "glob",
        pattern: "evil*",
        action: "observe",
        intent: "rate_limit",
        rateLimit: { requests: 30, periodSeconds: 10 },
        priority: 5,
        caseFold: true,
        enabled: true,
        note: "scraper",
        scopeKind: "global",
        scopeValues: [],
      },
    ]);
    expect(useToastStore().toasts.map((toast) => toast.key)).toContain("clients.rules.created");
  });

  it("validates locally and shows the server's refusal without its text", async () => {
    const server = routes().on("POST", base + "/rules", () => apiError(400, "invalid_request"));
    const { wrapper } = await open(server, "rules");
    await button(wrapper, text.rules.add).trigger("click");
    await wrapper.find("form").trigger("submit");
    expect(server.calls("POST", base + "/rules")).toHaveLength(0);
    expect(wrapper.text()).toContain(text.ruleForm.required);

    await field(wrapper, text.ruleForm.match).setValue("regex");
    await field(wrapper, text.ruleForm.pattern).setValue("(");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", base + "/rules")).toHaveLength(1);
    expect(wrapper.text()).toContain(text.ruleForm.invalid);
    expect(wrapper.text()).toContain("trace-invalid_request");
    expect(wrapper.text()).not.toContain("raw server text");
  });

  it("keeps the scope and window of a rule it cannot edit", async () => {
    const scoped: ClientRule = { ...observeRule, scopeKind: "user", scopeValues: ["u1"], window: { dailyStart: "22:00", dailyEnd: "06:00" } };
    const server = routes()
      .on("GET", base + "/rules", () => data([scoped]))
      .on("PUT", base + "/rules/:id", ({ body }) => data({ ...scoped, ...(body as object) }));
    const { wrapper } = await open(server, "rules");
    await button(wrapper, "Edit").trigger("click");
    expect(wrapper.text()).toContain(text.ruleForm.scopeKept);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", `${base}/rules/${observeRule.id}`)[0]?.body).toMatchObject({
      scopeKind: "user",
      scopeValues: ["u1"],
      window: { dailyStart: "22:00", dailyEnd: "06:00" },
      intent: "deny",
    });
  });
});

describe("client control: known clients", () => {
  it("kicks a client after confirming and reports the revoked sessions", async () => {
    const server = routes().on("POST", base + "/clients/:id/kick", () => data({ sessionsRevoked: 3 }));
    const { wrapper } = await open(server, "known");
    expect(wrapper.text()).toContain("Infuse");
    expect(wrapper.text()).toContain("198.51.100.7");
    await button(wrapper, text.known.kick).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/clients/${knownClient.id}/kick`)).toHaveLength(0);
    await button(wrapper, text.known.kickConfirm).trigger("click");
    await flushPromises();
    const calls = server.calls("POST", `${base}/clients/${knownClient.id}/kick`);
    expect(calls).toHaveLength(1);
    expect(calls[0]?.body).toEqual({});
    expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "clients.known.kicked", params: { count: 3, name: "Infuse" } }));
  });

  it("blocks a client only after the confirmation", async () => {
    const server = routes().on("POST", base + "/clients/:id/block", () => data({ ...observeRule, action: "deny" }, 201));
    const { wrapper } = await open(server, "known");
    await button(wrapper, text.known.block).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/clients/${knownClient.id}/block`)).toHaveLength(0);
    await button(wrapper, text.known.blockConfirm).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/clients/${knownClient.id}/block`)).toHaveLength(1);
  });

  it("renames and trusts a client", async () => {
    const server = routes().on("PATCH", base + "/clients/:id", ({ body }) => data({ ...knownClient, ...(body as object) }));
    const { wrapper } = await open(server, "known");
    await button(wrapper, text.known.rename).trigger("click");
    await field(wrapper, text.known.alias).setValue("  TV  ");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("TV");
    await button(wrapper, text.known.trust).trigger("click");
    await flushPromises();
    expect(server.calls("PATCH", `${base}/clients/${knownClient.id}`).map((request) => request.body)).toEqual([{ alias: "TV" }, { trusted: true }]);
    expect(wrapper.text()).toContain(text.known.trustedBadge);
  });

  it("pages with the cursor and shows the empty state", async () => {
    const second = { ...knownClient, id: "00000000-0000-4000-8000-0000000000d2", appName: "Swiftfin" };
    const server = routes().on("GET", base + "/clients", ({ url }) =>
      url.searchParams.get("cursor") === "c2"
        ? data({ clients: [second], pagination: { limit: 50, nextCursor: "" } })
        : data({ clients: [knownClient], pagination: { limit: 50, nextCursor: "c2" } }),
    );
    const { wrapper } = await open(server, "known");
    await button(wrapper, "Load more").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("Swiftfin");
    expect(wrapper.findAll("button").some((candidate) => candidate.text() === "Load more")).toBe(false);
    unmountAll();
    const empty = await open(
      routes().on("GET", base + "/clients", () => data({ clients: [], pagination: { limit: 50, nextCursor: "" } })),
      "known",
    );
    expect(empty.wrapper.text()).toContain(text.known.empty);
  });
});

describe("client control: hits", () => {
  it("shows statistics, the hit log, the filter and the export link", async () => {
    const server = routes();
    const { wrapper } = await open(server, "hits");
    expect(wrapper.text()).toContain(text.hits.total);
    expect(wrapper.text()).toContain("BadBot/1.0");
    // The top-rules chart names the rule instead of its ID.
    expect(wrapper.text()).toContain("User agent: BadBot/");
    expect(wrapper.findAll("table caption").map((caption) => caption.text())).toContain(text.hits.topRules);
    const link = wrapper.find("a[download]");
    expect(link.attributes("href")).toBe("/api/v1/client-control/hits/export");

    await field(wrapper, text.hits.period).setValue("168");
    await flushPromises();
    expect(server.calls("GET", base + "/stats").map((request) => request.url.searchParams.get("hours"))).toEqual(["24", "168"]);

    await field(wrapper, text.hits.mode).setValue("shadow");
    await flushPromises();
    expect(server.calls("GET", base + "/hits").at(-1)?.url.searchParams.get("mode")).toBe("shadow");
    expect(wrapper.find("a[download]").attributes("href")).toBe("/api/v1/client-control/hits/export?mode=shadow");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows an error for the statistics without server text", async () => {
    const server = routes().on("GET", base + "/stats", () => apiError(503, "not_ready"));
    const { wrapper } = await open(server, "hits");
    expect(wrapper.text()).toContain("trace-not_ready");
    expect(wrapper.text()).not.toContain("raw server text");
    expect(wrapper.text()).toContain("BadBot/1.0");
  });
});
