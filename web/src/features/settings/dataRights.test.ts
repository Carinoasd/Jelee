import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useAuthStore } from "@/stores/auth";
import { captureDownloads } from "@/test/downloads";
import { toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, regularUser, type RouteFetch } from "@/test/routeFetch";
import { exportFileName } from "./dataRightsApi";
import type { TwoFactorStatus } from "./twoFactorApi";

afterEach(() => {
  unmountAll();
});

const off: TwoFactorStatus = { available: true, enabled: false, pending: false, recoveryCodesRemaining: 0 };
const on: TwoFactorStatus = { available: true, enabled: true, enabledAt: "2026-10-04T00:00:00Z", pending: false, recoveryCodesRemaining: 10 };
const exportPath = `/api/v1/users/${regularUser.id}/data-export`;

function settingsServer(status: TwoFactorStatus = off): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/users/me/preferences", () => data({ theme: "system", density: "comfortable" }))
    .on("GET", "/api/v1/setup/status", () => data({ required: false }))
    .on("GET", `/api/v1/users/${regularUser.id}/two-factor`, () => data(status))
    .on("GET", `/api/v1/users/${regularUser.id}/app-passwords`, () => data([]));
}

function card(wrapper: VueWrapper) {
  const found = wrapper.find('section[aria-labelledby="settings-data-rights"]');
  expect(found.exists()).toBe(true);
  return found;
}

function inputIn(scope: ReturnType<VueWrapper["find"]>, text: string) {
  const id = scope.findAll("label").find((label) => label.text() === text)?.attributes("for");
  if (id === undefined) {
    throw new Error("no field " + text);
  }
  return scope.find(`[id="${id}"]`);
}

function buttonIn(scope: ReturnType<VueWrapper["find"]>, text: string) {
  const found = scope.findAll("button").find((candidate) => candidate.text() === text);
  if (found === undefined) {
    throw new Error("no button " + text);
  }
  return found;
}

describe("personal data settings", () => {
  it("names the export file by date", () => {
    expect(exportFileName(new Date(2026, 0, 5))).toBe("jelee-my-data-2026-01-05.ndjson");
  });

  it("downloads the export through the API and reports a busy server", async () => {
    let busy = false;
    const server = settingsServer().on("GET", exportPath, () =>
      busy ? apiError(409, "conflict") : new Response('{"type":"export","data":{}}\n{"type":"end","data":{"records":0}}\n', { headers: { "Content-Type": "application/x-ndjson" } }),
    );
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: regularUser, csrf: "csrf-token" });
    await flushPromises();
    const capture = captureDownloads();
    try {
      await buttonIn(card(wrapper), "Download my data").trigger("click");
      await flushPromises();
      expect(server.calls("GET", exportPath)).toHaveLength(1);
      expect(capture.downloads).toHaveLength(1);
      expect(capture.downloads[0]!.fileName).toMatch(/^jelee-my-data-\d{4}-\d{2}-\d{2}\.ndjson$/);
      expect(await capture.downloads[0]!.blob.text()).toContain('"type":"end"');
      expect(toastKeys(wrapper)).toContain("settings.dataRights.exported");

      busy = true;
      await buttonIn(card(wrapper), "Download my data").trigger("click");
      await flushPromises();
      expect(capture.downloads).toHaveLength(1);
      expect(card(wrapper).find("[role=alert]").exists()).toBe(true);
    } finally {
      capture.restore();
    }
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("asks for the password, an acknowledgement and a second press before deleting", async () => {
    const server = settingsServer().on("POST", "/api/v1/users/me/purge", ({ body }) =>
      (body as { password: string }).password === "right password" ? noContent() : apiError(400, "invalid_password"),
    );
    const { wrapper, router, pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser, csrf: "csrf-token" });
    await flushPromises();
    const section = card(wrapper);
    expect(section.text()).toContain("cannot be undone");
    // Nothing is sent while the form is incomplete.
    await buttonIn(section, "Delete my account").trigger("click");
    await flushPromises();
    await buttonIn(card(wrapper), "Delete permanently").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/purge")).toHaveLength(0);
    expect(card(wrapper).text()).toContain("Enter your password.");
    expect(card(wrapper).text()).toContain("Confirm that you understand");

    await inputIn(card(wrapper), "Password").setValue("wrong password");
    await card(wrapper).find("input[type=checkbox]").setValue(true);
    await buttonIn(card(wrapper), "Delete my account").trigger("click");
    await flushPromises();
    await buttonIn(card(wrapper), "Delete permanently").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/purge")[0]!.body).toEqual({ password: "wrong password" });
    expect(card(wrapper).text()).toContain("The current password is not correct.");
    expect(router.currentRoute.value.name).toBe("settings");
    expect((inputIn(card(wrapper), "Password").element as HTMLInputElement).value).toBe("");

    await inputIn(card(wrapper), "Password").setValue("right password");
    await buttonIn(card(wrapper), "Delete my account").trigger("click");
    await flushPromises();
    await buttonIn(card(wrapper), "Delete permanently").trigger("click");
    await flushPromises();
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("login");
    });
    expect(useAuthStore(pinia).user).toBeNull();
    expect(toastKeys(wrapper)).toContain("settings.dataRights.deleted");
  });

  it("sends the second factor when two-step verification is on", async () => {
    const server = settingsServer(on).on("POST", "/api/v1/users/me/purge", () => noContent());
    const { wrapper, router } = await mountView("/settings", { fetch: server.fetch, user: regularUser, csrf: "csrf-token" });
    await flushPromises();
    await inputIn(card(wrapper), "Password").setValue("right password");
    await card(wrapper).find("input[type=checkbox]").setValue(true);
    await buttonIn(card(wrapper), "Delete my account").trigger("click");
    await flushPromises();
    await buttonIn(card(wrapper), "Delete permanently").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/purge")).toHaveLength(0);
    await inputIn(card(wrapper), "Verification code").setValue("123 456");
    await buttonIn(card(wrapper), "Delete my account").trigger("click");
    await flushPromises();
    await buttonIn(card(wrapper), "Delete permanently").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/purge")[0]!.body).toEqual({ password: "right password", code: "123456" });
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("login");
    });
  });
});
