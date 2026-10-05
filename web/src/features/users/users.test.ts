import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { ratingLevels } from "@/features/access/api";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, json, regularUser } from "@/test/routeFetch";
import type { User } from "./api";
import { checkNewUser, checkTag, contentAccessBody, limitsBody, parseLimit, settingsOf } from "./form";
import { button, control, toastKeys } from "@/test/adminViews";

afterEach(() => {
  unmountAll();
});

const deletedUser: User = {
  ...regularUser,
  id: "00000000-0000-4000-8000-0000000000c3",
  name: "gone",
  displayName: "",
  disabled: true,
  allowNative: false,
  deletedAt: "2026-10-03T00:00:00Z",
};

const page = (users: User[], nextCursor = "") => data({ users, pagination: { nextCursor, limit: 50 } });

function listServer(users: User[]) {
  return createRouteFetch().on("GET", "/api/v1/users", ({ url }) =>
    page(url.searchParams.get("includeDeleted") === "true" ? users : users.filter((user) => user.deletedAt === undefined)),
  );
}

describe("account form helpers", () => {
  it("measures passwords and names in UTF-8 bytes", () => {
    const base = { name: "kid", profileName: "", locale: "en-US" as const, admin: false, hidden: false };
    expect(checkNewUser({ ...base, password: "a".repeat(11) }).password).toBe("users.create.passwordShort");
    // Four CJK characters are twelve UTF-8 bytes.
    expect(checkNewUser({ ...base, password: "密碼密碼" }).password).toBeNull();
    expect(checkNewUser({ ...base, password: "a".repeat(1025) }).password).toBe("users.create.passwordLong");
    expect(checkNewUser({ ...base, name: "  ", password: "a".repeat(12) }).name).toBe("users.form.nameRequired");
    expect(checkNewUser({ ...base, name: "名".repeat(43), password: "a".repeat(12) }).name).toBe("users.form.tooLong");
  });

  it("builds limit, content access and settings bodies", () => {
    expect(parseLimit("", 128)).toEqual({ ok: true, value: undefined });
    expect(parseLimit(" 0 ", 128)).toEqual({ ok: true, value: 0 });
    expect(parseLimit("129", 128)).toEqual({ ok: false });
    expect(parseLimit("1.5", 128)).toEqual({ ok: false });
    expect(limitsBody(undefined, 0)).toEqual({ maxKbps: 0 });
    expect(contentAccessBody("", "policy", [])).toEqual({ blockedTags: [] });
    expect(contentAccessBody("13", "show", ["x"])).toEqual({ parentalRatingMax: 13, blockUnrated: false, blockedTags: ["x"] });
    expect(settingsOf(regularUser)).toEqual({ name: "kid", displayName: "Kid", locale: "en-US", admin: false, hidden: false, disabled: false });
    expect(checkTag("Horror", ["horror"])).toBe("users.content.tagDuplicate");
    expect(checkTag(" ", [])).toBe("users.content.tagEmpty");
    expect(ratingLevels([{ code: "R", level: 17 }, { code: "G", level: 0 }, { code: "TV-MA", level: 17 }])).toEqual([
      { level: 0, codes: ["G"] },
      { level: 17, codes: ["R", "TV-MA"] },
    ]);
  });
});

describe("user list view", () => {
  it("shows a skeleton while loading", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/users", () => new Promise<Response>(() => undefined));
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    expect(wrapper.find("h1").text()).toBe("Users");
    expect(wrapper.find("[aria-busy=true]").exists()).toBe(true);
    expect(wrapper.find(".jl-skeleton").exists()).toBe(true);
  });

  it("lists accounts with links and reloads with deleted accounts", async () => {
    const server = listServer([adminUser, regularUser, deletedUser]);
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    const first = server.calls("GET", "/api/v1/users")[0]!.url.searchParams;
    expect(first.get("includeDeleted")).toBe("false");
    expect(first.get("limit")).toBe("50");
    const rows = wrapper.findAll("tbody tr");
    expect(rows).toHaveLength(2);
    expect(rows[1]!.find("th[scope=row] a").attributes("href")).toBe(`/admin/users/${regularUser.id}`);
    expect(rows[1]!.text()).toContain("Kid");
    expect(rows[1]!.text()).toContain("User");
    expect(rows[1]!.text()).toContain("Active");
    expect(rows[1]!.text()).toContain("Allowed");
    expect(wrapper.findAll("th[scope=col]")).toHaveLength(6);

    await control(wrapper, "Include deleted accounts").setValue(true);
    await flushPromises();
    expect(server.calls("GET", "/api/v1/users")[1]!.url.searchParams.get("includeDeleted")).toBe("true");
    expect(wrapper.findAll("tbody tr")).toHaveLength(3);
    expect(wrapper.findAll("tbody tr")[2]!.text()).toContain("Deleted");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("pages with the cursor", async () => {
    let calls = 0;
    const server = createRouteFetch().on("GET", "/api/v1/users", () => (++calls === 1 ? page([adminUser], adminUser.id) : page([regularUser])));
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    await button(wrapper, "Load more").trigger("click");
    await flushPromises();
    expect(server.calls("GET", "/api/v1/users")[1]!.url.searchParams.get("cursor")).toBe(adminUser.id);
    expect(wrapper.findAll("tbody tr")).toHaveLength(2);
    expect(wrapper.findAll("button").some((candidate) => candidate.text() === "Load more")).toBe(false);
  });

  it("shows the empty state", async () => {
    const server = listServer([]);
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    expect(wrapper.text()).toContain("No accounts yet");
    expect(wrapper.find("table").exists()).toBe(false);
  });

  it("shows a localized error with the trace ID and retries", async () => {
    let fail = true;
    const server = createRouteFetch().on("GET", "/api/v1/users", () => (fail ? apiError(500, "internal_error") : page([adminUser])));
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    expect(wrapper.text()).toContain("The server ran into an error");
    expect(wrapper.text()).toContain("trace-internal_error");
    expect(wrapper.text()).not.toContain("raw server text");
    fail = false;
    await button(wrapper, "Retry").trigger("click");
    await flushPromises();
    expect(wrapper.findAll("tbody tr")).toHaveLength(1);
  });
});

describe("create user form", () => {
  async function fill(wrapper: Awaited<ReturnType<typeof mountView>>["wrapper"], password = "correct horse battery") {
    await control(wrapper, "Sign-in name").setValue("  newbie ");
    await control(wrapper, "Shown name").setValue("New Person");
    await control(wrapper, "Password").setValue(password);
    await control<HTMLSelectElement>(wrapper, "Language").setValue("ja-JP");
    await control(wrapper, "Hidden account").setValue(true);
  }

  it("validates on the client without calling the server", async () => {
    const server = listServer([adminUser]);
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    await fill(wrapper, "short");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users")).toHaveLength(0);
    expect(wrapper.text()).toContain("Too short: at least 12 bytes.");
    expect(control(wrapper, "Password").attributes("aria-invalid")).toBe("true");
  });

  it("sends an Idempotency-Key, reuses it on retry and renews it after an edit", async () => {
    let attempt = 0;
    const server = listServer([adminUser]).on("POST", "/api/v1/users", ({ body }) => {
      attempt++;
      if (attempt === 1) {
        return apiError(503, "not_ready");
      }
      return json(201, { data: { ...regularUser, ...(body as object), id: "00000000-0000-4000-8000-0000000000d4" } });
    });
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    await fill(wrapper);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    const posts = () => server.calls("POST", "/api/v1/users");
    expect(posts()).toHaveLength(1);
    const key = posts()[0]!.headers.get("Idempotency-Key") ?? "";
    expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(posts()[0]!.body).toEqual({
      name: "newbie",
      displayName: "New Person",
      password: "correct horse battery",
      locale: "ja-JP",
      admin: false,
      hidden: true,
    });
    expect(wrapper.text()).toContain("The server is still starting");
    expect(wrapper.text()).not.toContain("raw server text");

    // Same submission again: same key, so the server can replay.
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(posts()).toHaveLength(2);
    expect(posts()[1]!.headers.get("Idempotency-Key")).toBe(key);
    expect(toastKeys(wrapper)).toContain("users.create.created");
    // The form is cleared and the list reloaded.
    expect(control(wrapper, "Sign-in name").element.value).toBe("");
    expect(server.calls("GET", "/api/v1/users").length).toBeGreaterThanOrEqual(2);

    await fill(wrapper);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(posts()).toHaveLength(3);
    expect(posts()[2]!.headers.get("Idempotency-Key")).not.toBe(key);
  });

  it("uses a new key when the form changes after a failure", async () => {
    const server = listServer([adminUser]).on("POST", "/api/v1/users", () => apiError(409, "conflict"));
    const { wrapper } = await mountView("/admin/users", { fetch: server.fetch, user: adminUser });
    await fill(wrapper);
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("This sign-in name is already in use.");
    expect(wrapper.text()).toContain("trace-conflict");
    await control(wrapper, "Sign-in name").setValue("other");
    await flushPromises();
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    const posts = server.calls("POST", "/api/v1/users");
    expect(posts).toHaveLength(2);
    expect(posts[1]!.headers.get("Idempotency-Key")).not.toBe(posts[0]!.headers.get("Idempotency-Key"));
  });
});
