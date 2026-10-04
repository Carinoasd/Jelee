import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import type { AppPassword, TwoFactorStatus } from "@/features/settings/twoFactorApi";
import { toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, noContent, regularUser } from "@/test/routeFetch";

afterEach(() => {
  unmountAll();
});

const base = `/api/v1/users/${regularUser.id}`;
const tv: AppPassword = { id: "60000000-0000-4000-8000-000000000001", name: "Living room TV", createdAt: "2026-10-01T00:00:00Z" };

function server(initial: TwoFactorStatus, passwords: AppPassword[]) {
  let status = initial;
  let list = passwords;
  return createRouteFetch()
    .on("GET", "/api/v1/users/:id", () => data(regularUser))
    .on("GET", `${base}/two-factor`, () => data(status))
    .on("DELETE", `${base}/two-factor`, () => {
      status = { available: true, enabled: false, pending: false, recoveryCodesRemaining: 0 };
      return noContent();
    })
    .on("GET", `${base}/app-passwords`, () => data(list))
    .on("DELETE", `${base}/app-passwords/:appPasswordId`, ({ params }) => {
      list = list.filter((entry) => entry.id !== params.appPasswordId);
      return noContent();
    });
}

describe("user two-step verification (administrator)", () => {
  it("shows the state, resets it after confirmation and revokes application passwords", async () => {
    const fake = server({ available: true, enabled: true, enabledAt: "2026-10-02T00:00:00Z", pending: false, recoveryCodesRemaining: 7 }, [tv]);
    const { wrapper } = await mountView(`/admin/users/${regularUser.id}`, { fetch: fake.fetch, user: adminUser });
    const card = wrapper.find('section[aria-labelledby="user-two-factor-title"]');
    expect(card.find("h2").text()).toBe("Two-step verification");
    expect(card.text()).toContain("On");
    expect(card.text()).toContain("Unused recovery codes: 7");
    expect(card.find("tbody").text()).toContain("Living room TV");

    const reset = () => card.findAll("button").find((button) => button.text() === "Reset two-step verification");
    await reset()!.trigger("click");
    expect(fake.calls("DELETE", `${base}/two-factor`)).toHaveLength(0);
    await card.findAll("button").find((button) => button.text() === "Reset now")!.trigger("click");
    await flushPromises();
    expect(fake.calls("DELETE", `${base}/two-factor`)).toHaveLength(1);
    expect(toastKeys(wrapper)).toContain("twoFactor.admin.resetDone");
    expect(card.text()).toContain("Off");
    expect(reset()).toBeUndefined();

    const row = card.find("tbody tr");
    await row.find("button").trigger("click");
    await row.findAll("button").find((button) => button.text() === "Revoke now")!.trigger("click");
    await flushPromises();
    expect(fake.calls("DELETE", `${base}/app-passwords/${tv.id}`)).toHaveLength(1);
    expect(card.text()).toContain("No application passwords yet.");
  });

  it("reports a failed reset with a localized message", async () => {
    const fake = server({ available: true, enabled: false, pending: true, recoveryCodesRemaining: 0 }, []).on("DELETE", `${base}/two-factor`, () =>
      apiError(403, "forbidden"),
    );
    const { wrapper } = await mountView(`/admin/users/${regularUser.id}`, { fetch: fake.fetch, user: adminUser });
    const card = wrapper.find('section[aria-labelledby="user-two-factor-title"]');
    expect(card.text()).toContain("Setup was started but not confirmed yet.");
    await card.findAll("button").find((button) => button.text() === "Reset two-step verification")!.trigger("click");
    await card.findAll("button").find((button) => button.text() === "Reset now")!.trigger("click");
    await flushPromises();
    expect(toastKeys(wrapper)).toContain("errors.forbidden");
  });
});
