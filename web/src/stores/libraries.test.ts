import { createPinia } from "pinia";
import { describe, expect, it } from "vitest";
import { createApp } from "vue";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import { createFakeServer } from "@/test/fakeServer";
import { useAuthStore } from "./auth";
import { useLibrariesStore } from "./libraries";

function setup() {
  const server = createFakeServer();
  const app = createApp({ render: () => null });
  app.provide(apiKey, createApiClient({ fetch: server.fetch }));
  app.use(createPinia());
  return { server, auth: app.runWithContext(() => useAuthStore()), store: app.runWithContext(() => useLibrariesStore()) };
}

describe("libraries store", () => {
  it("reports empty, success with pagination, and errors", async () => {
    const { server, auth, store } = setup();
    await auth.login("admin", "correct horse battery");

    await store.load();
    expect(store.state.status).toBe("empty");

    server.pageSize = 2;
    server.libraries = [1, 2, 3].map((n) => ({ id: String(n), name: `Library ${n}`, roots: n }));
    await store.load();
    expect(store.state.status).toBe("success");
    expect(store.libraries.map((library) => library.name)).toEqual(["Library 1", "Library 2"]);
    expect(store.nextCursor).toBe("2");
    await store.loadMore();
    expect(store.libraries).toHaveLength(3);
    expect(store.nextCursor).toBe("");

    server.failLibraries = true;
    await store.load();
    expect(store.state.status === "error" && store.state.error.code).toBe("not_ready");
  });

  it("forgets the user on logout", async () => {
    const { server, auth } = setup();
    await auth.login("admin", "correct horse battery");
    expect(auth.isAuthenticated).toBe(true);
    await auth.logout();
    expect(auth.isAuthenticated).toBe(false);
    expect(server.requests.at(-1)?.url).toMatch(/\/api\/v1\/auth\/logout$/);
  });
});
