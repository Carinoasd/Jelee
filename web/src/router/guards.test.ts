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
  it("lazy-loads every view", () => {
    for (const route of routes) {
      if ("component" in route && route.component !== undefined) {
        expect(typeof route.component).toBe("function");
      }
    }
  });
});
