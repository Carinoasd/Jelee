import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryBearerAuth } from "@/api/auth";
import { call, callNoContent } from "@/api/call";
import { createApiClient } from "@/api/client";
import { useAuthStore } from "@/stores/auth";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, regularUser } from "@/test/routeFetch";
import { utf8Length } from "./api";

afterEach(() => {
  unmountAll();
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.lang = "";
});

function profileServer() {
  return createRouteFetch()
    .on("PUT", "/api/v1/users/me/profile", ({ body }) => data({ ...regularUser, ...(body as object) }))
    .on("GET", "/api/v1/setup/status", () => data({ required: false }));
}

const field = (wrapper: Awaited<ReturnType<typeof mountView>>["wrapper"], label: string) => {
  const target = wrapper.findAll("label").find((candidate) => candidate.text() === label);
  const id = target?.attributes("for");
  if (id === undefined) {
    throw new Error("no field " + label);
  }
  return wrapper.find(`#${CSS.escape(id)}`);
};

describe("settings", () => {
  it("switches the theme between system, light and dark", async () => {
    const { wrapper } = await mountView("/settings", { fetch: profileServer().fetch, user: regularUser });
    const radios = wrapper.findAll("input[type=radio]");
    expect(radios).toHaveLength(3);
    expect(wrapper.find("fieldset legend").text()).toBe("Theme");
    await radios[2]!.setValue(true);
    expect(document.documentElement.dataset.theme).toBe("dark");
    await radios[1]!.setValue(true);
    expect(document.documentElement.dataset.theme).toBe("light");
    await radios[0]!.setValue(true);
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("applies the language at once and saves it with the whole profile", async () => {
    const server = profileServer();
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: { ...regularUser, hidden: true } });
    await field(wrapper, "Interface language").setValue("zh-TW");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/users/me/profile")[0]!.body).toEqual({ displayName: "Kid", hidden: true, locale: "zh-TW" });
    expect(wrapper.find("h1").text()).toBe("設定");
    expect(document.documentElement.lang).toBe("zh-TW");
  });

  it("saves the profile and updates the signed-in account", async () => {
    const server = profileServer();
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    await field(wrapper, "Display name").setValue("  Little one  ");
    await field(wrapper, "Hide my account from public user lists").setValue(true);
    await wrapper.findAll("form")[0]!.trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/users/me/profile")[0]!.body).toEqual({ displayName: "Little one", hidden: true, locale: "en-US" });
    expect((field(wrapper, "Display name").element as HTMLInputElement).value).toBe("Little one");
  });

  it("checks the new password before sending it", async () => {
    const server = profileServer().on("PUT", "/api/v1/users/me/password", () => noContent());
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    const form = wrapper.findAll("form")[1]!;
    await form.trigger("submit");
    expect(wrapper.text()).toContain("Enter your current password.");
    await field(wrapper, "Current password").setValue("old password!");
    await field(wrapper, "New password").setValue("short");
    await field(wrapper, "Repeat new password").setValue("short");
    await form.trigger("submit");
    expect(wrapper.text()).toContain("at least 12 bytes");
    expect(field(wrapper, "New password").attributes("aria-invalid")).toBe("true");
    await field(wrapper, "New password").setValue("long enough password");
    await form.trigger("submit");
    expect(wrapper.text()).toContain("The passwords do not match.");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/users/me/password")).toHaveLength(0);
    // Twelve bytes, but only four characters.
    expect(utf8Length("密碼密碼")).toBe(12);
  });

  it("keeps the user signed in when the current password is wrong", async () => {
    const server = profileServer().on("PUT", "/api/v1/users/me/password", () => apiError(401, "authentication_required"));
    const { wrapper, router } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    await field(wrapper, "Current password").setValue("wrong password");
    await field(wrapper, "New password").setValue("a brand new password");
    await field(wrapper, "Repeat new password").setValue("a brand new password");
    await wrapper.findAll("form")[1]!.trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/users/me/password")[0]!.body).toEqual({
      oldPassword: "wrong password",
      newPassword: "a brand new password",
    });
    expect(wrapper.find("[role=alert]").text()).toBe("The current password is not correct.");
    expect(router.currentRoute.value.name).toBe("settings");
    expect((field(wrapper, "Current password").element as HTMLInputElement).value).toBe("");
  });

  it("signs out after a successful change", async () => {
    const server = profileServer().on("PUT", "/api/v1/users/me/password", () => noContent());
    const { wrapper, router, pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    await field(wrapper, "Current password").setValue("old password!!");
    await field(wrapper, "New password").setValue("a brand new password");
    await field(wrapper, "Repeat new password").setValue("a brand new password");
    await wrapper.findAll("form")[1]!.trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/users/me/password")).toHaveLength(1);
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("login");
    });
    expect(useAuthStore(pinia).user).toBeNull();
  });
});

describe("credential checks and session expiry", () => {
  it("does not treat a wrong current password as an expired session", async () => {
    const onUnauthorized = vi.fn();
    const auth = createMemoryBearerAuth();
    const fetch = createRouteFetch()
      .on("PUT", "/api/v1/users/me/password", () => apiError(401, "authentication_required"))
      .on("GET", "/api/v1/users/me", () => apiError(401, "authentication_required")).fetch;
    const { client } = createApiClient({ fetch, auth, onUnauthorized, baseUrl: "http://localhost" });
    auth.establish({ token: "x".repeat(43) } as Parameters<typeof auth.establish>[0]);
    await expect(callNoContent(client.PUT("/api/v1/users/me/password", { body: { oldPassword: "a", newPassword: "b" } }))).rejects.toMatchObject({
      status: 401,
    });
    expect(auth.hasCredential()).toBe(true);
    expect(onUnauthorized).not.toHaveBeenCalled();
    await expect(call(client.GET("/api/v1/users/me", {}))).rejects.toMatchObject({ status: 401 });
    expect(auth.hasCredential()).toBe(false);
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });
});
