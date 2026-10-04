import { afterEach, describe, expect, it, vi } from "vitest";
import { createFakeServer } from "@/test/fakeServer";
import { createCookieCsrfAuth, createMemoryBearerAuth, csrfHeader, type SessionGrant } from "./auth";
import { call, callNoContent } from "./call";
import { createApiClient } from "./client";
import type { components } from "./schema";

/** The fake server's accounts have no second factor, so login grants a session. */
function sessionOf(data: SessionGrant | components["schemas"]["SecondFactorChallenge"]): SessionGrant {
  if ("secondFactorRequired" in data) {
    throw new Error("unexpected second factor challenge");
  }
  return data;
}

afterEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

describe("memory bearer auth", () => {
  it("keeps the token in memory only and attaches it to requests", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const server = createFakeServer();
    const { client, auth } = createApiClient({
      fetch: server.fetch,
      locale: () => "zh-TW",
      auth: createMemoryBearerAuth(),
    });
    const grant = await call(
      client.POST("/api/v1/auth/login", { body: { name: "admin", password: "correct horse battery" } }),
    );
    auth.establish(sessionOf(grant.data));
    await call(client.GET("/api/v1/libraries", { params: { query: { limit: 50 } } }));

    const last = server.requests.at(-1)!;
    expect(last.headers.get("Authorization")).toBe("Bearer " + server.token);
    expect(last.headers.get("Accept-Language")).toBe("zh-TW");
    expect(last.credentials).toBe("same-origin");
    expect(setItem).not.toHaveBeenCalled();
    expect(localStorage.length + sessionStorage.length).toBe(0);
    expect(document.cookie).toBe("");
  });

  it("clears the credential and notifies on 401", async () => {
    const server = createFakeServer();
    const onUnauthorized = vi.fn();
    const auth = createMemoryBearerAuth();
    const { client } = createApiClient({ fetch: server.fetch, auth, onUnauthorized });
    auth.establish({ token: "x".repeat(43) } as Parameters<typeof auth.establish>[0]);
    await expect(call(client.GET("/api/v1/libraries", {}))).rejects.toMatchObject({
      code: "authentication_required",
      status: 401,
    });
    expect(auth.hasCredential()).toBe(false);
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it("cannot resume after a reload", () => {
    const auth = createMemoryBearerAuth();
    expect(auth.survivesReload).toBe(false);
    expect(() => {
      auth.resume({ csrf: "c".repeat(43) });
    }).toThrow(TypeError);
  });

  it("rejects a grant without a token", () => {
    const auth = createMemoryBearerAuth();
    expect(() => {
      auth.establish({ token: "" } as Parameters<typeof auth.establish>[0]);
    }).toThrow(TypeError);
    expect(auth.hasCredential()).toBe(false);
  });

  it("maps fetch failures to network_error", async () => {
    const { client } = createApiClient({ fetch: () => Promise.reject(new TypeError("offline")) });
    await expect(call(client.GET("/api/v1/libraries", {}))).rejects.toMatchObject({ code: "network_error" });
  });
});

describe("cookie + CSRF auth", () => {
  it("is the default, never sends the bearer token and keeps nothing in storage", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const server = createFakeServer();
    const { client, auth } = createApiClient({ fetch: server.fetch });
    expect(auth.kind).toBe("cookie-csrf");
    const grant = await call(
      client.POST("/api/v1/auth/login", { body: { name: "admin", password: "correct horse battery" } }),
    );
    auth.establish(sessionOf(grant.data));
    await call(client.GET("/api/v1/libraries", { params: { query: { limit: 50 } } }));

    const read = server.requests.at(-1)!;
    expect(read.headers.get("Authorization")).toBeNull();
    // Safe methods never carry the CSRF token.
    expect(read.headers.get(csrfHeader)).toBeNull();
    expect(read.credentials).toBe("same-origin");

    await call(client.DELETE("/api/v1/users/{id}/sessions/{sessionID}", {
      params: { path: { id: server.user.id, sessionID: "00000000-0000-4000-8000-0000000000ff" } },
    })).catch(() => undefined);
    const write = server.requests.at(-1)!;
    expect(write.method).toBe("DELETE");
    expect(write.headers.get(csrfHeader)).toBe(server.csrf);
    expect(write.headers.get("Authorization")).toBeNull();

    expect(setItem).not.toHaveBeenCalled();
    expect(localStorage.length + sessionStorage.length).toBe(0);
    expect(document.cookie).toBe("");
    expect(JSON.stringify(auth)).not.toContain(server.token);
    expect(JSON.stringify(auth)).not.toContain(server.csrf);
  });

  it("is rejected by the server without the CSRF header", async () => {
    const server = createFakeServer();
    server.cookie = true;
    const { client } = createApiClient({ fetch: server.fetch, auth: createCookieCsrfAuth() });
    await expect(callNoContent(client.POST("/api/v1/auth/logout", { body: {} }))).rejects.toMatchObject({
      code: "csrf_failed",
      status: 403,
    });
  });

  it("resumes with a fresh CSRF token and clears on 401", async () => {
    const server = createFakeServer();
    const onUnauthorized = vi.fn();
    const auth = createCookieCsrfAuth();
    const { client } = createApiClient({ fetch: server.fetch, auth, onUnauthorized });
    expect(auth.survivesReload).toBe(true);
    auth.resume({ csrf: server.csrf });
    expect(auth.hasCredential()).toBe(true);
    // The browser no longer holds a valid cookie.
    await expect(call(client.GET("/api/v1/users/me", {}))).rejects.toMatchObject({ status: 401 });
    expect(auth.hasCredential()).toBe(false);
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it("rejects grants and responses without a CSRF token", () => {
    const auth = createCookieCsrfAuth();
    expect(() => {
      auth.establish({ token: "x".repeat(43) } as Parameters<typeof auth.establish>[0]);
    }).toThrow(TypeError);
    expect(() => {
      auth.resume({ csrf: "" });
    }).toThrow(TypeError);
    expect(auth.hasCredential()).toBe(false);
  });
});
