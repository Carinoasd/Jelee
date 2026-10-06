import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/schema";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, json, noContent, regularUser, type RouteFetch } from "@/test/routeFetch";
import type { ContentAccessView, User } from "./api";
import { button, control, hasButton, toastKeys } from "@/test/adminViews";

type Session = components["schemas"]["Session"];

afterEach(() => {
  unmountAll();
});

const kid = regularUser;
const base = `/api/v1/users/${kid.id}`;
const libraryA = "10000000-0000-4000-8000-00000000000a";
const libraryB = "10000000-0000-4000-8000-00000000000b";
const series = "20000000-0000-4000-8000-000000000001";
const movie = "20000000-0000-4000-8000-000000000002";

const session: Session = {
  id: "30000000-0000-4000-8000-000000000001",
  userId: kid.id,
  clientKind: "native",
  deviceName: "Living room",
  client: "Jelee TV",
  createdAt: "2026-10-01T00:00:00Z",
  expiresAt: "2026-11-01T00:00:00Z",
  lastIp: "192.0.2.4",
};

const access: ContentAccessView = {
  parentalRatingMax: 13,
  blockedTags: ["horror"],
  blockedKeywords: ["Spoiler"],
  windows: [{ weekdays: [1, 2], start: "21:00", end: "07:00", timeZone: "Asia/Taipei", ratingMax: 13 }],
  rules: [{ itemId: series, libraryId: libraryA, kind: "Series", title: "Night Show", effect: "hide", createdAt: "2026-10-02T00:00:00Z" }],
};

/** Every read of the detail page answers; writes are added per test. */
function detailServer(user: User = kid): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/users/:id", () => data(user))
    .on("GET", "/api/v1/users/:id/delivery-limits", () => data({ maxStreams: 2 }))
    .on("GET", "/api/v1/users/:id/sessions", () => data([session]))
    .on("GET", "/api/v1/libraries", () =>
      data({
        libraries: [
          { id: libraryA, name: "Films", roots: 1 },
          { id: libraryB, name: "Shows", roots: 1 },
        ],
        pagination: { nextCursor: "", limit: 50 },
      }),
    )
    .on("GET", "/api/v1/users/:id/libraries", () => data([{ libraryId: libraryA, name: "Films" }]))
    .on("GET", "/api/v1/users/:id/content-access", () => data(access))
    .on("GET", "/api/v1/access/parental-ratings", () =>
      data([
        { code: "G", level: 0 },
        { code: "PG-13", level: 13 },
        { code: "R", level: 17 },
      ]),
    );
}

async function open(server: RouteFetch, user: User = kid) {
  return mountView(`/admin/users/${user.id}`, { fetch: server.fetch, user: adminUser });
}

function within(wrapper: VueWrapper, titleId: string) {
  const section = wrapper.find(`section[aria-labelledby="${titleId}"]`);
  expect(section.exists()).toBe(true);
  return section;
}

describe("user detail view", () => {
  it("loads every section without playback markup", async () => {
    const server = detailServer();
    const { wrapper } = await open(server);
    expect(wrapper.find("h1").text()).toBe("Kid");
    expect(wrapper.find("h1").attributes("tabindex")).toBe("-1");
    for (const path of [base, `${base}/delivery-limits`, `${base}/sessions`, `${base}/libraries`, `${base}/content-access`]) {
      expect(server.calls("GET", path)).toHaveLength(1);
    }
    expect(control(wrapper, "Simultaneous viewing").element.value).toBe("2");
    expect(control(wrapper, "Bandwidth (kbit/s)").element.value).toBe("");
    expect(control(wrapper, "Films").element.checked).toBe(true);
    expect(control(wrapper, "Shows").element.checked).toBe(false);
    expect(control<HTMLSelectElement>(wrapper, "Rating ceiling").element.value).toBe("13");
    expect(wrapper.text()).toContain("Age 13 (PG-13)");
    expect(wrapper.text()).toContain("Night Show");
    expect(wrapper.text()).toContain("Living room · Jelee TV");
    expect(wrapper.text()).toContain("non-administrator sees no library at all");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows a localized error with the trace ID and retries", async () => {
    let fail = true;
    const server = detailServer().on("GET", "/api/v1/users/:id", () => (fail ? apiError(404, "not_found") : data(kid)));
    const { wrapper } = await open(server);
    expect(wrapper.text()).toContain("This content does not exist or you cannot access it.");
    expect(wrapper.text()).toContain("trace-not_found");
    expect(wrapper.text()).not.toContain("raw server text");
    fail = false;
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    expect(wrapper.find("h1").text()).toBe("Kid");
  });

  it("disables only after the second press and sends every setting", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id", ({ body }) => data({ ...kid, ...(body as object) }));
    const { wrapper } = await open(server);
    await button(wrapper, "Disable account").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", base)).toHaveLength(0);
    expect(wrapper.text()).toContain("signs this account out on every device");
    await button(wrapper, "Disable now").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", base)).toHaveLength(1);
    expect(server.calls("PUT", base)[0]!.body).toEqual({
      name: "kid",
      displayName: "Kid",
      locale: "en-US",
      admin: false,
      hidden: false,
      disabled: true,
    });
    expect(toastKeys(wrapper)).toContain("users.status.disabledDone");
    expect(hasButton(wrapper, "Enable account")).toBe(true);
  });

  it("saves edited settings, confirming a role change", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id", ({ body }) => data({ ...kid, ...(body as object) }));
    const { wrapper } = await open(server);
    await control(wrapper, "Shown name").setValue("Kiddo");
    await control(wrapper, "Administrator").setValue(true);
    const settings = within(wrapper, "user-settings-title");
    // Enter in a field must not bypass the confirmation either.
    await settings.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", base)).toHaveLength(0);
    await settings.findAll("button").find((candidate) => candidate.text() === "Save")!.trigger("click");
    await flushPromises();
    expect(server.calls("PUT", base)).toHaveLength(0);
    await button(wrapper, "Save and sign out").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", base)[0]!.body).toEqual({
      name: "kid",
      displayName: "Kiddo",
      locale: "en-US",
      admin: true,
      hidden: false,
      disabled: false,
    });
  });

  it("withdraws native login only after confirmation", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id/native", ({ body }) => data({ ...kid, ...(body as object) }));
    const { wrapper } = await open(server);
    await button(wrapper, "Withdraw native login").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/native`)).toHaveLength(0);
    await button(wrapper, "Withdraw and sign out apps").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/native`)[0]!.body).toEqual({ allowNative: false });
    // The native session is gone from the list.
    expect(wrapper.text()).not.toContain("Living room");
  });

  it("deletes only after confirmation, then offers restore", async () => {
    let deleted = false;
    const server = detailServer()
      .on("GET", "/api/v1/users/:id", () => data(deleted ? { ...kid, deletedAt: "2026-10-04T00:00:00Z" } : kid))
      .on("DELETE", "/api/v1/users/:id", () => {
        deleted = true;
        return noContent();
      })
      .on("POST", "/api/v1/users/:id/restore", () => {
        deleted = false;
        return data(kid);
      });
    const { wrapper } = await open(server);
    await button(wrapper, "Delete account").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", base)).toHaveLength(0);
    expect(wrapper.text()).toContain("Watch history and personal data are removed for good.");
    await button(wrapper, "Delete permanently").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", base)).toHaveLength(1);
    expect(wrapper.text()).toContain("This account is deleted.");
    expect(wrapper.find("section[aria-labelledby=user-settings-title]").exists()).toBe(false);
    await button(wrapper, "Restore account").trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/restore`)[0]!.body).toEqual({});
    expect(wrapper.find("section[aria-labelledby=user-settings-title]").exists()).toBe(true);
  });

  it("unlocks with an empty object body", async () => {
    const server = detailServer().on("POST", "/api/v1/users/:id/unlock", () => noContent());
    const { wrapper } = await open(server);
    await button(wrapper, "Unlock").trigger("click");
    await flushPromises();
    expect(server.calls("POST", `${base}/unlock`)[0]!.body).toEqual({});
    expect(toastKeys(wrapper)).toContain("users.lifecycle.unlocked");
  });

  it("revokes all sessions only after confirmation", async () => {
    const server = detailServer().on("DELETE", "/api/v1/users/:id/sessions", () => noContent());
    const { wrapper } = await open(server);
    await button(wrapper, "Sign out everywhere").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `${base}/sessions`)).toHaveLength(0);
    await button(wrapper, "Revoke all sessions").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `${base}/sessions`)).toHaveLength(1);
    expect(wrapper.text()).toContain("No active sessions");
  });

  it("saves library grants and delivery limits", async () => {
    const server = detailServer()
      .on("PUT", "/api/v1/users/:id/libraries", () => noContent())
      .on("PUT", "/api/v1/users/:id/delivery-limits", ({ body }) => data(body));
    const { wrapper } = await open(server);
    await control(wrapper, "Shows").setValue(true);
    await within(wrapper, "user-libraries-title").find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", `${base}/libraries`)[0]!.body).toEqual({ libraryIds: [libraryA, libraryB] });

    await control(wrapper, "Simultaneous viewing").setValue("");
    await control(wrapper, "Bandwidth (kbit/s)").setValue("abc");
    const limits = within(wrapper, "user-limits-title").find("form");
    await limits.trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", `${base}/delivery-limits`)).toHaveLength(0);
    await control(wrapper, "Bandwidth (kbit/s)").setValue("0");
    await limits.trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", `${base}/delivery-limits`)[0]!.body).toEqual({ maxKbps: 0 });
  });

  it("puts the content access settings", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id/content-access", ({ body }) =>
      data({ blockedKeywords: [], ...(body as object), rules: access.rules, windows: access.windows }),
    );
    const { wrapper } = await open(server);
    await control<HTMLSelectElement>(wrapper, "Rating ceiling").setValue("17");
    await control<HTMLSelectElement>(wrapper, "Items without a rating").setValue("hide");
    await control(wrapper, "Tag or genre to block").setValue("  Gore ");
    await within(wrapper, "user-content-title").find("form").trigger("submit");
    await flushPromises();
    await control(wrapper, "Tag or genre to block").setValue("HORROR");
    await within(wrapper, "user-content-title").find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("This tag is already in the list.");
    const save = () => within(wrapper, "user-content-title").findAll("button").find((candidate) => candidate.text() === "Save")!;
    await save().trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access`)[0]!.body).toEqual({
      parentalRatingMax: 17,
      blockUnrated: true,
      blockedTags: ["horror", "Gore"],
      blockedKeywords: ["Spoiler"],
    });

    await wrapper.find("[aria-label='Remove tag horror']").trigger("click");
    await control<HTMLSelectElement>(wrapper, "Rating ceiling").setValue("");
    await control<HTMLSelectElement>(wrapper, "Items without a rating").setValue("policy");
    await save().trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access`)[1]!.body).toEqual({ blockedTags: ["Gore"], blockedKeywords: ["Spoiler"] });
  });

  it("edits the blocked keywords and always sends them with the content settings", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id/content-access", ({ body }) =>
      data({ ...(body as object), blockedKeywords: (body as { blockedKeywords?: string[] }).blockedKeywords ?? [], rules: access.rules, windows: access.windows }),
    );
    const { wrapper } = await open(server);
    const content = within(wrapper, "user-content-title");
    expect(content.text()).toContain("Spoiler");
    const keywordForm = () => content.findAll("form")[1]!;
    // Full-width letters fold into the same keyword.
    await control(wrapper, "Keyword to block").setValue("ＳＰＯＩＬＥＲ");
    await keywordForm().trigger("submit");
    expect(wrapper.text()).toContain("This keyword is already in the list.");
    await control(wrapper, "Keyword to block").setValue("  Ending ");
    await keywordForm().trigger("submit");
    await wrapper.find("[aria-label='Remove keyword Spoiler']").trigger("click");
    await control(wrapper, "Start of window 1").setValue("20:00");
    const save = content.findAll("button").find((candidate) => candidate.text() === "Save")!;
    await save.trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access`)[0]!.body).toEqual({
      parentalRatingMax: 13,
      blockedTags: ["horror"],
      blockedKeywords: ["Ending"],
    });
    expect(toastKeys(wrapper)).toContain("users.content.saved");
    // Saving the settings above keeps the unsaved time window edit below.
    expect(control(wrapper, "Start of window 1").element.value).toBe("20:00");
  });

  it("saves the time windows with a ceiling or hiding everything", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id/content-access/windows", ({ body }) =>
      data({ ...access, windows: (body as { windows: unknown[] }).windows }),
    );
    const { wrapper } = await open(server);
    const windows = within(wrapper, "user-windows-title");
    expect(control(wrapper, "Start of window 1").element.value).toBe("21:00");
    expect(control(wrapper, "Time zone of window 1").element.value).toBe("Asia/Taipei");
    expect(control<HTMLSelectElement>(wrapper, "Rating ceiling of window 1").element.value).toBe("13");
    const saveButton = () => windows.findAll("button").find((candidate) => candidate.text() === "Save time windows")!;
    expect(saveButton().attributes("disabled")).toBeDefined();

    await button(wrapper, "Add time window").trigger("click");
    await flushPromises();
    expect(control(wrapper, "Time zone of window 2").element.value).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    await control(wrapper, "Start of window 2").setValue("25:00");
    await control(wrapper, "End of window 2").setValue("24:00");
    await control(wrapper, "Time zone of window 2").setValue("Europe/Berlin");
    await saveButton().trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access/windows`)).toHaveLength(0);
    expect(windows.text()).toContain("Enter a time as HH:MM, 00:00 to 23:59.");

    await control(wrapper, "Start of window 2").setValue("09:30");
    const days = windows.findAll("fieldset")[1]!;
    for (const day of ["Sat", "Sun"]) {
      const label = days.findAll("label").find((candidate) => candidate.text() === day)!;
      await days.find(`[id="${label.attributes("for")}"]`).setValue(true);
    }
    await control<HTMLSelectElement>(wrapper, "Inside window 1").setValue("hide");
    await saveButton().trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access/windows`)[0]!.body).toEqual({
      windows: [
        { weekdays: [1, 2], start: "21:00", end: "07:00", timeZone: "Asia/Taipei" },
        { weekdays: [0, 6], start: "09:30", end: "24:00", timeZone: "Europe/Berlin" },
      ],
    });
    expect(toastKeys(wrapper)).toContain("contentRules.windows.saved");

    await wrapper.find("[aria-label='Remove window 1']").trigger("click");
    await wrapper.find("[aria-label='Remove window 1']").trigger("click");
    await saveButton().trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access/windows`)[1]!.body).toEqual({ windows: [] });
    expect(windows.text()).toContain("No time windows.");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("deletes an item rule only after confirmation and adds one from search", async () => {
    const server = detailServer()
      .on("DELETE", "/api/v1/users/:id/content-access/items/:itemId", () => noContent())
      .on("GET", "/api/v1/items", ({ url }) =>
        json(200, {
          data: url.searchParams.get("q") === "space" ? [{ id: movie, libraryId: libraryA, kind: "Movie", title: "Space Trip", productionYear: 2020 }] : [],
          pagination: { nextCursor: "", limit: 10, offset: 0, total: 1 },
        }),
      )
      .on("PUT", "/api/v1/users/:id/content-access/items/:itemId", ({ params, body }) =>
        data({ itemId: params.itemId, libraryId: libraryA, kind: "Movie", title: "Space Trip", createdAt: "2026-10-04T00:00:00Z", ...(body as object) }),
      );
    const { wrapper } = await open(server);
    const rules = within(wrapper, "user-rules-title");
    await rules.findAll("button").find((candidate) => candidate.text() === "Delete")!.trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `${base}/content-access/items/${series}`)).toHaveLength(0);
    await button(wrapper, "Delete rule").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `${base}/content-access/items/${series}`)).toHaveLength(1);
    expect(wrapper.text()).toContain("No item rules.");

    await control(wrapper, "Find an item by title").setValue("nothing");
    await wrapper.find("form[role=search]").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("No matching items");
    await control(wrapper, "Find an item by title").setValue(" space ");
    await wrapper.find("form[role=search]").trigger("submit");
    await flushPromises();
    const search = server.calls("GET", "/api/v1/items").at(-1)!.url.searchParams;
    expect(search.get("q")).toBe("space");
    expect(search.get("limit")).toBe("10");
    await control<HTMLSelectElement>(wrapper, "Effect of the new rule").setValue("allow");
    await button(wrapper, "Add rule").trigger("click");
    await flushPromises();
    expect(server.calls("PUT", `${base}/content-access/items/${movie}`)[0]!.body).toEqual({ effect: "allow" });
    expect(wrapper.find("table caption").exists()).toBe(true);
    expect(hasButton(wrapper, "Replace rule")).toBe(true);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("shows a deleted account with restore only", async () => {
    const gone: User = { ...kid, deletedAt: "2026-10-03T00:00:00Z" };
    const server = detailServer(gone);
    const { wrapper } = await open(server, gone);
    expect(hasButton(wrapper, "Restore account")).toBe(true);
    expect(hasButton(wrapper, "Delete account")).toBe(false);
    expect(server.calls("GET", `${base}/content-access`)).toHaveLength(0);
    expect(server.calls("GET", `${base}/sessions`)).toHaveLength(0);
  });

  it("reports a failed write with a localized toast", async () => {
    const server = detailServer().on("PUT", "/api/v1/users/:id", () => apiError(409, "last_admin"));
    const { wrapper } = await open(server);
    await button(wrapper, "Disable account").trigger("click");
    await button(wrapper, "Disable now").trigger("click");
    await flushPromises();
    expect(toastKeys(wrapper)).toContain("errors.lastAdmin");
    expect(hasButton(wrapper, "Disable account")).toBe(true);
  });

  it("signs the administrator out after revoking their own sessions", async () => {
    const server = detailServer(adminUser).on("DELETE", "/api/v1/users/:id/sessions", () => noContent());
    const { wrapper, router } = await open(server, adminUser);
    expect(wrapper.text()).toContain("This is your own account.");
    await button(wrapper, "Sign out everywhere").trigger("click");
    await button(wrapper, "Revoke all sessions").trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/users/${adminUser.id}/sessions`)).toHaveLength(1);
    // The sign-in view is lazy-loaded, so navigation settles a little later.
    await vi.waitUntil(() => router.currentRoute.value.name === "login");
    expect(toastKeys(wrapper)).toContain("users.signedOutSelf");
  });
});
