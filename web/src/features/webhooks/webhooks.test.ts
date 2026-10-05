import { flushPromises, type DOMWrapper, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/schema";
import en from "@/i18n/en-US/webhooks.json";
import errorsEn from "@/i18n/en-US/errors.json";
import { useToastStore } from "@/stores/toasts";
import { useWebhooksStore } from "@/stores/webhooks";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, type RouteFetch } from "@/test/routeFetch";

type Webhook = components["schemas"]["Webhook"];
type WebhookDelivery = components["schemas"]["WebhookDelivery"];
type WebhookEvent = Webhook["events"][number];

const text = en.webhooks;
const secretValue = "whsec_0123456789abcdef";
// A catalog as the server would send it; the UI must not know it in advance.
const catalog: WebhookEvent[] = ["media.added", "scan.completed", "user.login"];

const hook: Webhook = {
  id: "00000000-0000-4000-8000-0000000000e1",
  name: "Discord relay",
  url: "https://hooks.example.com/jelee",
  enabled: true,
  events: [],
  headerNames: ["Authorization"],
  timeoutSeconds: 10,
  retry: { baseDelaySeconds: 10, jitter: 0.2, maxAttempts: 8, maxDelaySeconds: 3600 },
  pending: 2,
  dead: 1,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};

const dead: WebhookDelivery = {
  id: "00000000-0000-4000-8000-0000000000f1",
  webhookId: hook.id,
  eventId: "evt_dead_1",
  eventType: "scan.completed",
  state: "dead",
  attempts: 8,
  replays: 0,
  lastOutcome: "http",
  lastStatus: 500,
  occurredAt: "2026-10-03T00:00:00Z",
  createdAt: "2026-10-03T00:00:00Z",
};

const pendingDelivery: WebhookDelivery = {
  ...dead,
  id: "00000000-0000-4000-8000-0000000000f2",
  eventId: "evt_pending_1",
  state: "pending",
  attempts: 1,
  nextAttemptAt: "2026-10-04T00:00:00Z",
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
    .on("GET", "/api/v1/webhooks", () => data({ webhooks: [hook], events: catalog }))
    .on("GET", "/api/v1/webhooks/:id", () => data(hook))
    .on("GET", "/api/v1/webhooks/:id/deliveries", () => data({ deliveries: [dead, pendingDelivery], pagination: { limit: 50, nextCursor: "" } }));
}

afterEach(() => {
  unmountAll();
});

describe("webhooks list", () => {
  it("lists endpoints with their state and counts", async () => {
    const { wrapper } = await mountView("/admin/webhooks", { fetch: routes().fetch, user: adminUser });
    expect(wrapper.findAll("h1")).toHaveLength(1);
    expect(wrapper.find("h1").text()).toBe(text.title);
    const row = wrapper.find("tbody tr");
    expect(row.text()).toContain("Discord relay");
    expect(row.text()).toContain("https://hooks.example.com/jelee");
    expect(row.text()).toContain(text.allEvents);
    expect(row.text()).toContain(text.on);
    expect(row.find("a").attributes("href")).toBe(`/admin/webhooks/${hook.id}`);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows the empty and the error state", async () => {
    const empty = await mountView("/admin/webhooks", {
      fetch: routes().on("GET", "/api/v1/webhooks", () => data({ webhooks: [], events: catalog })).fetch,
      user: adminUser,
    });
    expect(empty.wrapper.text()).toContain(text.empty);
    unmountAll();

    let fail = true;
    const server = routes().on("GET", "/api/v1/webhooks", () => (fail ? apiError(500, "internal_error") : data({ webhooks: [hook], events: catalog })));
    const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    expect(wrapper.text()).toContain(errorsEn.errors.server);
    expect(wrapper.text()).toContain("trace-internal_error");
    expect(wrapper.text()).not.toContain("raw server text");
    fail = false;
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("Discord relay");
  });

  it("creates an endpoint and shows its secret exactly once", async () => {
    const server = routes().on("POST", "/api/v1/webhooks", ({ body }) =>
      data({ webhook: { ...hook, ...(body as object), id: "00000000-0000-4000-8000-0000000000e2", headerNames: ["X-Token"] }, secret: secretValue }, 201),
    );
    const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.add).trigger("click");
    // Event checkboxes come from the server's catalog.
    expect(wrapper.findAll("fieldset input[type=checkbox]").length).toBeGreaterThanOrEqual(catalog.length);
    await field(wrapper, text.form.name).setValue("  Home Assistant ");
    await field(wrapper, text.form.url).setValue("https://ha.example.com/hook");
    await field(wrapper, text.form.timeout).setValue("15");
    await field(wrapper, "user.login").setValue(true);
    await field(wrapper, "media.added").setValue(true);
    await button(wrapper, text.form.addHeader).trigger("click");
    await field(wrapper, text.form.headerName).setValue("X-Token");
    await field(wrapper, text.form.headerValue).setValue("s3cr3t");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/webhooks").map((request) => request.body)).toEqual([
      {
        name: "Home Assistant",
        url: "https://ha.example.com/hook",
        enabled: true,
        events: ["media.added", "user.login"],
        timeoutSeconds: 15,
        headers: { "X-Token": "s3cr3t" },
      },
    ]);
    const notice = wrapper.find("[data-secret]");
    expect(notice.text()).toBe(secretValue);
    expect(document.activeElement?.id).toBe("webhook-secret-title");

    await button(wrapper, text.secret.done).trigger("click");
    expect(wrapper.html()).not.toContain(secretValue);
    expect(useWebhooksStore().secret).toBeNull();
    // The new endpoint stays listed; nothing brings the secret back.
    expect(wrapper.text()).toContain("Home Assistant");
  });

  it("forgets an undismissed secret when the page is left", async () => {
    const server = routes().on("POST", "/api/v1/webhooks", () => data({ webhook: hook, secret: secretValue }, 201));
    const { wrapper, router } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.add).trigger("click");
    await field(wrapper, text.form.name).setValue("Relay");
    await field(wrapper, text.form.url).setValue("https://relay.example.com/");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.html()).toContain(secretValue);
    await router.push(`/admin/webhooks/${hook.id}`);
    await flushPromises();
    expect(useWebhooksStore().secret).toBeNull();
    expect(wrapper.html()).not.toContain(secretValue);
  });

  it("copies the secret or asks for a manual copy", async () => {
    const writeText = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    try {
      const server = routes().on("POST", "/api/v1/webhooks", () => data({ webhook: hook, secret: secretValue }, 201));
      const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
      await button(wrapper, text.add).trigger("click");
      await field(wrapper, text.form.name).setValue("Relay");
      await field(wrapper, text.form.url).setValue("https://relay.example.com/");
      await wrapper.find("form").trigger("submit");
      await flushPromises();
      await button(wrapper, text.secret.copy).trigger("click");
      await flushPromises();
      expect(writeText).toHaveBeenCalledWith(secretValue);
      expect(wrapper.text()).toContain(text.secret.copied);
      await button(wrapper, text.secret.copy).trigger("click");
      await flushPromises();
      expect(wrapper.text()).toContain(text.secret.copyFailed);
    } finally {
      Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  it("refuses a non-HTTPS address locally and maps the server's refusal", async () => {
    const server = routes().on("POST", "/api/v1/webhooks", () => apiError(400, "webhook_target_denied"));
    const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.add).trigger("click");
    await field(wrapper, text.form.name).setValue("Relay");
    await field(wrapper, text.form.url).setValue("http://relay.example.com/");
    await wrapper.find("form").trigger("submit");
    expect(server.calls("POST", "/api/v1/webhooks")).toHaveLength(0);
    expect(wrapper.text()).toContain(text.form.urlInvalid);
    await field(wrapper, text.form.url).setValue("https://10.0.0.1/");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/webhooks")).toHaveLength(1);
    expect(wrapper.text()).toContain(errorsEn.errors.webhookTargetDenied);
    expect(wrapper.text()).not.toContain("raw server text");
  });

  it("disables an endpoint by replacing its settings without touching headers", async () => {
    const server = routes().on("PUT", "/api/v1/webhooks/:id", ({ body }) => data({ ...hook, ...(body as object) }));
    const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.disable).trigger("click");
    await flushPromises();
    const body = server.calls("PUT", `/api/v1/webhooks/${hook.id}`)[0]?.body;
    expect(body).toEqual({ name: hook.name, url: hook.url, enabled: false, events: [], timeoutSeconds: 10, retry: hook.retry });
    expect(wrapper.text()).toContain(text.off);
  });

  it("deletes an endpoint only after the confirmation", async () => {
    const server = routes().on("DELETE", "/api/v1/webhooks/:id", () => noContent());
    const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    await button(wrapper, "Delete").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/webhooks/${hook.id}`)).toHaveLength(0);
    await button(wrapper, text.deleteConfirm).trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/webhooks/${hook.id}`)).toHaveLength(1);
    expect(useToastStore().toasts.map((toast) => toast.key)).toContain("webhooks.deleted");
  });

  it("sends a test event and shows the outcome", async () => {
    const server = routes().on("POST", "/api/v1/webhooks/:id/test", () =>
      data({ outcome: "http", statusCode: 502, durationMs: 120, eventId: "evt_test" }),
    );
    const { wrapper } = await mountView("/admin/webhooks", { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.test.send).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `/api/v1/webhooks/${hook.id}/test`)[0]?.body).toEqual({});
    expect(wrapper.text()).toContain("Test: HTTP error, HTTP 502, 120 ms");
  });
});

describe("webhook detail", () => {
  const path = `/admin/webhooks/${hook.id}`;

  it("shows settings and the delivery log; pending deliveries cannot be resent", async () => {
    const { wrapper } = await mountView(path, { fetch: routes().fetch, user: adminUser });
    expect(wrapper.findAll("h1")).toHaveLength(1);
    expect(wrapper.find("h1").text()).toContain("Discord relay");
    expect(wrapper.text()).toContain("Authorization");
    const rows = wrapper.findAll("tbody tr");
    expect(rows[0]?.text()).toContain("scan.completed");
    expect(rows[0]?.text()).toContain("HTTP error (500)");
    expect(rows[0]?.findAll("button").some((candidate) => candidate.text() === text.deliveries.resend)).toBe(true);
    expect(rows[1]?.findAll("button").some((candidate) => candidate.text() === text.deliveries.resend)).toBe(false);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("resends a dead delivery only after the confirmation", async () => {
    const server = routes().on("POST", "/api/v1/webhooks/:id/deliveries/:deliveryId/replay", () =>
      data({ ...dead, state: "pending", replays: 1, attempts: 0 }, 202),
    );
    const { wrapper } = await mountView(path, { fetch: server.fetch, user: adminUser });
    const target = `/api/v1/webhooks/${hook.id}/deliveries/${dead.id}/replay`;
    await button(wrapper, text.deliveries.resend).trigger("click");
    await flushPromises();
    expect(server.calls("POST", target)).toHaveLength(0);
    expect(wrapper.find('[role="alert"]').text()).toContain("evt_dead_1");
    await button(wrapper, text.deliveries.resendConfirm).trigger("click");
    await flushPromises();
    expect(server.calls("POST", target).map((request) => request.body)).toEqual([{}]);
    expect(wrapper.findAll("tbody tr")[0]?.text()).toContain(text.deliveries.states.pending);
  });

  it("expands the attempt history of a delivery", async () => {
    const server = routes().on("GET", "/api/v1/webhooks/:id/deliveries/:deliveryId", () =>
      data({
        ...dead,
        history: [
          { round: 1, attempt: 2, outcome: "http", statusCode: 500, startedAt: "2026-10-03T00:01:00Z", finishedAt: "2026-10-03T00:01:01Z" },
          { round: 1, attempt: 1, outcome: "timeout", startedAt: "2026-10-03T00:00:00Z", finishedAt: "2026-10-03T00:00:10Z" },
        ],
      }),
    );
    const { wrapper } = await mountView(path, { fetch: server.fetch, user: adminUser });
    const toggle = button(wrapper, text.deliveries.history);
    expect(toggle.attributes("aria-expanded")).toBe("false");
    await toggle.trigger("click");
    await flushPromises();
    expect(toggle.attributes("aria-expanded")).toBe("true");
    const panel = wrapper.find(`[id="${toggle.attributes("aria-controls") ?? ""}"]`);
    expect(panel.text()).toContain(text.outcome.timeout);
    expect(panel.text()).toContain("1 / 2");
  });

  it("filters deliveries by state", async () => {
    const server = routes();
    const { wrapper } = await mountView(path, { fetch: server.fetch, user: adminUser });
    await field(wrapper, text.deliveries.filter).setValue("dead");
    await flushPromises();
    const calls = server.calls("GET", `/api/v1/webhooks/${hook.id}/deliveries`);
    expect(calls.at(-1)?.url.searchParams.get("state")).toBe("dead");
  });

  it("saves settings, keeping stored headers unless replaced", async () => {
    const server = routes().on("PUT", "/api/v1/webhooks/:id", ({ body }) => data({ ...hook, ...(body as object) }));
    const { wrapper } = await mountView(path, { fetch: server.fetch, user: adminUser });
    await field(wrapper, text.form.name).setValue("Relay 2");
    await field(wrapper, "scan.completed").setValue(true);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", `/api/v1/webhooks/${hook.id}`)[0]?.body).toEqual({
      name: "Relay 2",
      url: hook.url,
      enabled: true,
      events: ["scan.completed"],
      timeoutSeconds: 10,
      retry: hook.retry,
    });
    expect(wrapper.find("h1").text()).toContain("Relay 2");

    await field(wrapper, text.form.replaceHeaders).setValue(true);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", `/api/v1/webhooks/${hook.id}`)[1]?.body).toMatchObject({ headers: {} });
  });

  it("rotates the secret after confirming and shows the new one once", async () => {
    const server = routes().on("POST", "/api/v1/webhooks/:id/rotate-secret", () =>
      data({ webhook: { ...hook, previousSecretUntil: "2026-10-05T00:00:00Z" }, secret: secretValue }),
    );
    const { wrapper } = await mountView(path, { fetch: server.fetch, user: adminUser });
    await field(wrapper, text.rotate.grace).setValue("3600");
    await button(wrapper, text.rotate.action).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `/api/v1/webhooks/${hook.id}/rotate-secret`)).toHaveLength(0);
    await button(wrapper, text.rotate.confirm).trigger("click");
    await flushPromises();
    expect(server.calls("POST", `/api/v1/webhooks/${hook.id}/rotate-secret`)[0]?.body).toEqual({ graceSeconds: 3600 });
    expect(wrapper.find("[data-secret]").text()).toBe(secretValue);
    await button(wrapper, text.secret.done).trigger("click");
    expect(wrapper.html()).not.toContain(secretValue);
  });

  it("shows a localized error for a missing endpoint", async () => {
    const server = routes().on("GET", "/api/v1/webhooks/:id", () => apiError(404, "not_found"));
    const { wrapper } = await mountView(path, { fetch: server.fetch, user: adminUser });
    expect(wrapper.text()).toContain(errorsEn.errors.notFound);
    expect(wrapper.text()).toContain("trace-not_found");
    expect(wrapper.text()).not.toContain("raw server text");
    expect(wrapper.findAll("h1")).toHaveLength(1);
  });
});
