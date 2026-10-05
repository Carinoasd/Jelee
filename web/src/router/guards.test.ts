import { describe, expect, it } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { navigationGuard } from "./guards";
import { safeRedirect } from "./redirect";
import { routes } from "./routes";

const router = createRouter({ history: createMemoryHistory(), routes });
const resolve = (path: string) => router.resolve(path);

describe("navigation guard", () => {
  it("sends anonymous users to /login with the requested path", () => {
    expect(navigationGuard(resolve("/libraries?x=1"), { isAuthenticated: false, isAdmin: false })).toEqual({
      name: "login",
      query: { redirect: "/libraries?x=1" },
    });
  });

  it("lets public routes through and keeps signed-in users off /login", () => {
    const anonymous = { isAuthenticated: false, isAdmin: false };
    expect(navigationGuard(resolve("/login"), anonymous)).toBe(true);
    expect(navigationGuard(resolve("/does/not/exist"), anonymous)).toBe(true);
    expect(navigationGuard(resolve("/login?redirect=//evil.example"), { isAuthenticated: true, isAdmin: false })).toBe(
      "/libraries",
    );
  });

  it("blocks admin-only routes for regular users", () => {
    const admin = router.resolve("/libraries");
    const adminRoute = { ...admin, meta: { admin: true } };
    expect(navigationGuard(adminRoute, { isAuthenticated: true, isAdmin: false })).toEqual({ name: "forbidden" });
    expect(navigationGuard(adminRoute, { isAuthenticated: true, isAdmin: true })).toBe(true);
  });

  it("protects the browsing and account pages and keeps deep links", () => {
    const anonymous = { isAuthenticated: false, isAdmin: false };
    for (const path of ["/libraries/abc", "/items/abc?x=1", "/account"]) {
      expect(navigationGuard(resolve(path), anonymous)).toEqual({ name: "login", query: { redirect: path } });
      expect(navigationGuard(resolve(path), { isAuthenticated: true, isAdmin: false })).toBe(true);
    }
    expect(resolve("/libraries/abc").name).toBe("library");
    expect(resolve("/items/abc").params).toEqual({ itemId: "abc" });
  });

  it("guards every administration page by the merged admin meta", () => {
    const regular = { isAuthenticated: true, isAdmin: false };
    const admin = { isAuthenticated: true, isAdmin: true };
    const anonymous = { isAuthenticated: false, isAdmin: false };
    const paths = ["/admin", "/admin/users", "/admin/users/u1", "/admin/access", "/admin/clients", "/admin/webhooks", "/admin/webhooks/w1", "/admin/stats"];
    for (const path of paths) {
      const target = resolve(path);
      expect(target.meta.admin, path).toBe(true);
      expect(navigationGuard(target, regular), path).toEqual({ name: "forbidden" });
      expect(navigationGuard(target, admin), path).toBe(true);
      expect(navigationGuard(target, anonymous), path).toEqual({ name: "login", query: { redirect: path } });
    }
    expect(resolve("/admin/users/u1").params).toEqual({ userId: "u1" });
  });

  it("lets every signed-in user search, see their statistics and settings", () => {
    for (const path of ["/search?q=x", "/stats", "/settings"]) {
      const target = resolve(path);
      expect(target.meta.admin, path).toBeUndefined();
      expect(navigationGuard(target, { isAuthenticated: true, isAdmin: false }), path).toBe(true);
      expect(navigationGuard(target, { isAuthenticated: false, isAdmin: false }), path).toEqual({ name: "login", query: { redirect: path } });
    }
  });

  it("resolves unknown paths to the 404 view", () => {
    expect(resolve("/nothing/here").name).toBe("not-found");
  });
});

describe("safeRedirect", () => {
  it.each([
    ["/libraries?x=1", "/libraries?x=1"],
    ["//evil.example/", "/libraries"],
    ["https://evil.example/", "/libraries"],
    ["/\\evil.example", "/libraries"],
    ["/lib\u0000raries", "/libraries"],
    ["/login?redirect=/x", "/libraries"],
    [["/a"], "/libraries"],
    [undefined, "/libraries"],
  ])("%j -> %s", (input, expected) => {
    expect(safeRedirect(input)).toBe(expected);
  });
});

describe("routes", () => {
  it("lazy-loads every view, nested ones included", () => {
    const all = routes.flatMap((route) => [route, ...(route.children ?? [])]);
    expect(all.length).toBeGreaterThan(routes.length);
    for (const route of all) {
      if ("component" in route && route.component !== undefined) {
        expect(typeof route.component).toBe("function");
      }
    }
  });

  it("has no playback route", () => {
    const all = routes.flatMap((route) => [route, ...(route.children ?? [])]);
    for (const route of all) {
      expect(route.path).not.toMatch(/play|stream|cast|pip|player/i);
    }
  });

  it("keeps share guests on their share and the item pages", () => {
    const guest = { isAuthenticated: true, isAdmin: false, isGuest: true };
    for (const path of ["/", "/libraries", "/settings", "/account", "/search", "/admin/users"]) {
      expect(navigationGuard(resolve(path), guest)).toEqual({ name: "shared" });
    }
    expect(navigationGuard(resolve("/shared"), guest)).toBe(true);
    expect(navigationGuard(resolve("/items/abc"), guest)).toBe(true);
    expect(navigationGuard(resolve("/share"), guest)).toBe(true);
    expect(navigationGuard(resolve("/share"), { isAuthenticated: false, isAdmin: false })).toBe(true);
    expect(navigationGuard(resolve("/shared"), { isAuthenticated: false, isAdmin: false })).toEqual({ name: "login", query: { redirect: "/shared" } });
  });
});
