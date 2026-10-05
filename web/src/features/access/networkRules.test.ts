import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "@/api/schema";
import en from "@/i18n/en-US/networkRules.json";
import { button, control, toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, type RouteFetch } from "@/test/routeFetch";
import { parseCidrs } from "./api";

type NetworkRule = components["schemas"]["NetworkRule"];

const text = en.networkRules;
const base = "/api/v1/access/network-rules";
const libraryA = "10000000-0000-4000-8000-00000000000a";
const libraryB = "10000000-0000-4000-8000-00000000000b";

const rule: NetworkRule = {
  id: "70000000-0000-4000-8000-000000000001",
  libraryId: libraryA,
  libraryName: "Movies",
  network: "lan",
  cidrs: ["192.168.1.0/24"],
  clientKinds: ["web"],
  includeAdmins: false,
  enabled: true,
  note: "home only",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};

function server(rules: NetworkRule[] = [rule]): RouteFetch {
  return createRouteFetch()
    .on("GET", base, () => data(rules))
    .on("GET", "/api/v1/libraries", () =>
      data({
        libraries: [
          { id: libraryA, name: "Movies", roots: 1 },
          { id: libraryB, name: "Shows", roots: 1 },
        ],
        pagination: { limit: 50, nextCursor: "" },
      }),
    );
}

function cidrField(wrapper: VueWrapper) {
  return control<HTMLTextAreaElement>(wrapper, text.form.cidrs);
}

afterEach(() => {
  unmountAll();
});

describe("network rules", () => {
  it("splits the address field into one entry per line", () => {
    expect(parseCidrs(" 10.0.0.0/8 \r\n\n2001:db8::/32\n10.0.0.0/8\n  ")).toEqual(["10.0.0.0/8", "2001:db8::/32"]);
    expect(parseCidrs("")).toEqual([]);
  });

  it("lists the rules on the access page's second tab", async () => {
    const { wrapper } = await mountView("/admin/access/network", { fetch: server().fetch, user: adminUser });
    expect(wrapper.find("a[href='/admin/access']").exists()).toBe(true);
    const row = wrapper.find("tbody tr");
    expect(row.text()).toContain("Movies");
    expect(row.text()).toContain(text.network.lan);
    expect(row.text()).toContain("192.168.1.0/24");
    expect(row.text()).toContain(text.kind.web);
    expect(row.text()).toContain("home only");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("creates a rule with one CIDR per line and the ticked kinds", async () => {
    const stored: NetworkRule[] = [];
    const routes = server(stored).on("POST", base, ({ body }) => {
      const created: NetworkRule = { ...rule, ...(body as object), id: "new", libraryName: "Shows" };
      stored.push(created);
      return data(created, 201);
    });
    const { wrapper } = await mountView("/admin/access/network", { fetch: routes.fetch, user: adminUser });
    expect(wrapper.text()).toContain(text.empty);
    await button(wrapper, text.add).trigger("click");
    await flushPromises();

    await wrapper.find("form").trigger("submit");
    expect(routes.calls("POST", base)).toHaveLength(0);
    expect(wrapper.text()).toContain(text.form.libraryRequired);

    await control<HTMLSelectElement>(wrapper, text.form.library).setValue(libraryB);
    await control<HTMLSelectElement>(wrapper, text.form.network).setValue("wan");
    await cidrField(wrapper).setValue("203.0.113.0/24\n\n  198.51.100.7  \n2001:db8::/32\n");
    await control(wrapper, text.kind.native).setValue(true);
    await control(wrapper, text.form.includeAdmins).setValue(true);
    await control(wrapper, text.form.note).setValue("travel");
    await wrapper.find("form").trigger("submit");
    await flushPromises();

    expect(routes.calls("POST", base).map((request) => request.body)).toEqual([
      {
        libraryId: libraryB,
        network: "wan",
        cidrs: ["203.0.113.0/24", "198.51.100.7", "2001:db8::/32"],
        clientKinds: ["native"],
        includeAdmins: true,
        enabled: true,
        note: "travel",
      },
    ]);
    expect(toastKeys(wrapper)).toContain("networkRules.created");
    expect(wrapper.find("form").exists()).toBe(false);
    expect(wrapper.findAll("tbody tr")).toHaveLength(1);
  });

  it("refuses more than 64 entries locally and shows the server's refusal without its text", async () => {
    const routes = server().on("PUT", `${base}/:id`, () => apiError(400, "invalid_request"));
    const { wrapper } = await mountView("/admin/access/network", { fetch: routes.fetch, user: adminUser });
    await button(wrapper, "Edit").trigger("click");
    await flushPromises();
    expect(cidrField(wrapper).element.value).toBe("192.168.1.0/24");
    expect(control(wrapper, text.kind.web).element.checked).toBe(true);

    await cidrField(wrapper).setValue(Array.from({ length: 65 }, (_, i) => `10.0.${i}.0/24`).join("\n"));
    await wrapper.find("form").trigger("submit");
    expect(routes.calls("PUT", `${base}/${rule.id}`)).toHaveLength(0);
    expect(wrapper.text()).toContain("At most 64 addresses or prefixes.");
    expect(cidrField(wrapper).attributes("aria-invalid")).toBe("true");

    await cidrField(wrapper).setValue("not-an-address");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(routes.calls("PUT", `${base}/${rule.id}`)[0]!.body).toMatchObject({ libraryId: libraryA, cidrs: ["not-an-address"], clientKinds: ["web"] });
    expect(wrapper.text()).toContain(text.form.invalid);
    expect(wrapper.text()).toContain("trace-invalid_request");
    expect(wrapper.text()).not.toContain("raw server text");
  });

  it("deletes a rule only after the confirmation", async () => {
    const routes = server().on("DELETE", `${base}/:id`, () => noContent());
    const { wrapper } = await mountView("/admin/access/network", { fetch: routes.fetch, user: adminUser });
    await button(wrapper, "Delete").trigger("click");
    await flushPromises();
    expect(routes.calls("DELETE", `${base}/${rule.id}`)).toHaveLength(0);
    expect(wrapper.text()).toContain(text.deletePrompt);
    routes.on("GET", base, () => data([]));
    await button(wrapper, text.deleteConfirm).trigger("click");
    await flushPromises();
    expect(routes.calls("DELETE", `${base}/${rule.id}`)).toHaveLength(1);
    expect(toastKeys(wrapper)).toContain("networkRules.deleted");
    expect(wrapper.text()).toContain(text.empty);
  });
});
