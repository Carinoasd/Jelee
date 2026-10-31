import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryBearerAuth } from "@/api/auth";
import { callNoContent } from "@/api/call";
import { createApiClient } from "@/api/client";
import { useAuthStore } from "@/stores/auth";
import { defaultDisplay, usePreferencesStore } from "@/stores/preferences";
import { useToastStore } from "@/stores/toasts";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, noContent, regularUser } from "@/test/routeFetch";
import { utf8Length } from "./api";

afterEach(() => {
  unmountAll();
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("data-palette");
  document.documentElement.lang = "";
});

const plainDisplay = { palette: null, posterRatio: "portrait", posterSize: "medium", listDensity: "comfortable", view: "poster" } as const;

function profileServer() {
  return createRouteFetch()
    .on("PUT", "/api/v1/users/me/profile", ({ body }) => data({ ...regularUser, ...(body as object) }))
    .on("GET", "/api/v1/users/me/preferences", () => data({ theme: "system", density: "compact", layout: null, display: null }))
    .on("PUT", "/api/v1/users/me/preferences", ({ body }) => data(body))
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
  it("switches the theme between system, light, dark and the presets and stores it with the account", async () => {
    const server = profileServer();
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    expect(server.calls("GET", "/api/v1/users/me/preferences")).toHaveLength(1);
    const theme = wrapper.findAll("fieldset")[0]!;
    expect(theme.find("legend").text()).toBe("Theme");
    const radios = theme.findAll("input[type=radio]");
    expect(theme.findAll("label").map((label) => label.text())).toEqual(["Match system", "Light", "Dark", "Sepia", "High contrast"]);
    await radios[2]!.setValue(true);
    expect(document.documentElement.dataset.theme).toBe("dark");
    await radios[1]!.setValue(true);
    expect(document.documentElement.dataset.theme).toBe("light");
    await radios[3]!.setValue(true);
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(document.documentElement.dataset.palette).toBe("sepia");
    await radios[0]!.setValue(true);
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(document.documentElement.hasAttribute("data-palette")).toBe(false);
    await flushPromises();
    // The whole record is sent; the stored density and layout are kept, and
    // a preset is stored as its scheme.
    expect(server.calls("PUT", "/api/v1/users/me/preferences").map((request) => request.body)).toEqual([
      { theme: "dark", density: "compact", layout: null, display: null },
      { theme: "light", density: "compact", layout: null, display: null },
      { theme: "light", density: "compact", layout: null, display: { ...plainDisplay, palette: "sepia" } },
      { theme: "system", density: "compact", layout: null, display: { ...plainDisplay, palette: null } },
    ]);
    expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "settings.theme.saved" }));
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("stores the density and the display choices with the account", async () => {
    localStorage.clear();
    const server = profileServer();
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    const group = (legend: string) => wrapper.findAll("fieldset").find((fieldset) => fieldset.find("legend").text() === legend)!;
    expect(document.documentElement.dataset.density).toBe("compact");
    await group("Density").findAll("input")[0]!.setValue(true);
    await flushPromises();
    expect(document.documentElement.hasAttribute("data-density")).toBe(false);
    expect(server.calls("PUT", "/api/v1/users/me/preferences").at(-1)!.body).toEqual({ theme: "system", density: "comfortable", layout: null, display: null });

    await group("Poster shape").findAll("input")[2]!.setValue(true);
    await group("Poster size").findAll("input")[0]!.setValue(true);
    await group("List density").findAll("input")[1]!.setValue(true);
    await group("Default view").findAll("input")[1]!.setValue(true);
    await flushPromises();
    const root = document.documentElement.dataset;
    expect([root.poster, root.posterSize, root.listDensity]).toEqual(["landscape", "small", "compact"]);
    const puts = server.calls("PUT", "/api/v1/users/me/preferences");
    expect(puts).toHaveLength(5);
    expect(puts.at(-1)!.body).toEqual({
      theme: "system",
      density: "comfortable",
      layout: null,
      display: { palette: null, posterRatio: "landscape", posterSize: "small", listDensity: "compact", view: "list" },
    });
    // Nothing is kept in this browser any more.
    expect(localStorage.getItem(`jelee.ui.v1.display-preferences.${regularUser.id}`)).toBeNull();
    for (const name of ["data-density", "data-poster", "data-poster-size", "data-list-density"]) {
      document.documentElement.removeAttribute(name);
    }
    localStorage.clear();
  });

  it("reads the display choices from the account and lets its preset win over the stored theme", async () => {
    const legacyKey = `jelee.ui.v1.display-preferences.${regularUser.id}`;
    localStorage.setItem(legacyKey, JSON.stringify({ palette: "contrast", view: "list" }));
    const server = profileServer().on("GET", "/api/v1/users/me/preferences", () =>
      data({ theme: "light", density: "comfortable", layout: null, display: { ...plainDisplay, palette: "sepia", posterSize: "large" } }),
    );
    const { pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    const preferences = usePreferencesStore(pinia);
    expect(preferences.theme).toBe("sepia");
    expect(preferences.display).toEqual({ ...plainDisplay, palette: "sepia", posterSize: "large" });
    expect(document.documentElement.dataset.palette).toBe("sepia");
    expect(document.documentElement.dataset.posterSize).toBe("large");
    // The palette wins even when another client left a different scheme in theme.
    server.on("GET", "/api/v1/users/me/preferences", () => data({ theme: "dark", density: "comfortable", layout: null, display: { ...plainDisplay, palette: "sepia" } }));
    await preferences.load();
    expect(preferences.theme).toBe("sepia");
    // The account's values win over an older local copy, which is dropped.
    expect(preferences.display.view).toBe("poster");
    expect(server.calls("PUT", "/api/v1/users/me/preferences")).toHaveLength(0);
    expect(localStorage.getItem(legacyKey)).toBeNull();
    localStorage.clear();
  });

  it("moves the display choices an earlier version kept in this browser to the account once", async () => {
    const legacyKey = `jelee.ui.v1.display-preferences.${regularUser.id}`;
    localStorage.setItem(legacyKey, JSON.stringify({ palette: "contrast", posterRatio: "square", view: "list", listDensity: "bogus" }));
    const server = profileServer().on("GET", "/api/v1/users/me/preferences", () => data({ theme: "dark", density: "compact", layout: null, display: null }));
    const { pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    await flushPromises();
    const expected = { palette: "contrast", posterRatio: "square", posterSize: "medium", listDensity: "comfortable", view: "list" };
    expect(server.calls("PUT", "/api/v1/users/me/preferences").map((request) => request.body)).toEqual([
      { theme: "dark", density: "compact", layout: null, display: expected },
    ]);
    expect(localStorage.getItem(legacyKey)).toBeNull();
    const preferences = usePreferencesStore(pinia);
    expect(preferences.theme).toBe("contrast");
    expect(preferences.display).toEqual(expected);
    // A second load finds nothing to move.
    await preferences.load();
    expect(server.calls("PUT", "/api/v1/users/me/preferences")).toHaveLength(1);
  });

  it("keeps the local copy when moving it fails and does not move a preset that disagrees with the stored scheme", async () => {
    const legacyKey = `jelee.ui.v1.display-preferences.${regularUser.id}`;
    localStorage.setItem(legacyKey, JSON.stringify({ palette: "sepia", view: "list" }));
    const server = profileServer()
      .on("GET", "/api/v1/users/me/preferences", () => data({ theme: "dark", density: "comfortable", layout: null, display: null }))
      .on("PUT", "/api/v1/users/me/preferences", () => apiError(503, "account_busy"));
    const { pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/users/me/preferences")[0]!.body).toEqual({
      theme: "dark",
      density: "comfortable",
      layout: null,
      display: { ...plainDisplay, palette: null, view: "list" },
    });
    expect(localStorage.getItem(legacyKey)).not.toBeNull();
    expect(usePreferencesStore(pinia).theme).toBe("dark");
    localStorage.clear();
  });

  it("sends nothing and keeps display unset for an account that never chose one", async () => {
    localStorage.clear();
    const server = profileServer().on("GET", "/api/v1/users/me/preferences", () => data({ theme: "dark", density: "comfortable", layout: null }));
    const { pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    await flushPromises();
    expect(usePreferencesStore(pinia).display).toEqual(defaultDisplay);
    expect(server.calls("PUT", "/api/v1/users/me/preferences")).toHaveLength(0);
  });

  it("applies the stored theme on load and keeps a chosen theme when saving fails", async () => {
    const server = profileServer()
      .on("GET", "/api/v1/users/me/preferences", () => data({ theme: "dark", density: "comfortable" }))
      .on("PUT", "/api/v1/users/me/preferences", () => apiError(503, "account_busy"));
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect((wrapper.findAll("input[type=radio]")[2]!.element as HTMLInputElement).checked).toBe(true);
    await wrapper.findAll("input[type=radio]")[1]!.setValue(true);
    await flushPromises();
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "errors.busy", tone: "danger" }));
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
    const server = profileServer().on("PUT", "/api/v1/users/me/password", () => apiError(400, "invalid_password"));
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
  it("keeps the session on invalid_password and expires it on any 401", async () => {
    const onUnauthorized = vi.fn();
    const auth = createMemoryBearerAuth();
    let passwordStatus = 400;
    const fetch = createRouteFetch().on("PUT", "/api/v1/users/me/password", () =>
      passwordStatus === 400 ? apiError(400, "invalid_password") : apiError(401, "authentication_required"),
    ).fetch;
    const { client } = createApiClient({ fetch, auth, onUnauthorized, baseUrl: "http://localhost" });
    auth.establish({ token: "x".repeat(43) } as Parameters<typeof auth.establish>[0]);
    const change = () => callNoContent(client.PUT("/api/v1/users/me/password", { body: { oldPassword: "a", newPassword: "b" } }));
    await expect(change()).rejects.toMatchObject({ status: 400, code: "invalid_password" });
    expect(auth.hasCredential()).toBe(true);
    expect(onUnauthorized).not.toHaveBeenCalled();
    // No path is exempt any more: a 401 on the password route is an expiry.
    passwordStatus = 401;
    await expect(change()).rejects.toMatchObject({ status: 401 });
    expect(auth.hasCredential()).toBe(false);
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });
});

describe("preferences store", () => {
  it("keeps a signed-out choice in memory without calling the server", async () => {
    const server = profileServer();
    const { pinia } = await mountView("/login", { fetch: server.fetch, user: null });
    const preferences = usePreferencesStore(pinia);
    await preferences.setTheme("dark");
    expect(preferences.theme).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(server.requests).toHaveLength(0);
  });

  it("drops a load that a newer choice overtook", async () => {
    let release: (response: Response) => void = () => undefined;
    const server = profileServer().on(
      "GET",
      "/api/v1/users/me/preferences",
      () => new Promise<Response>((resolve) => (release = resolve)),
    );
    const { pinia } = await mountView("/settings", { fetch: server.fetch, user: regularUser });
    const preferences = usePreferencesStore(pinia);
    await preferences.setTheme("light");
    release(data({ theme: "dark", density: "comfortable" }));
    await flushPromises();
    expect(preferences.theme).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});
