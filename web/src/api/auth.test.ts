import { afterEach, describe, expect, it, vi } from "vitest";
import { createFakeServer } from "@/test/fakeServer";
import { createMemoryBearerAuth } from "./auth";
import { call } from "./call";
import { createApiClient } from "./client";

afterEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

describe("memory bearer auth", () => {
  it("keeps the token in memory only and attaches it to requests", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const server = createFakeServer();
    const { client, auth } = createApiClient({ fetch: server.fetch, locale: () => "zh-TW" });
    const grant = await call(
      client.POST("/api/v1/auth/login", { body: { name: "admin", password: "correct horse battery" } }),
    );
    auth.establish(grant.data);
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
