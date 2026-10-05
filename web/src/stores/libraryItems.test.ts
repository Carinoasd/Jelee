import { createPinia } from "pinia";
import { describe, expect, it } from "vitest";
import { createApp } from "vue";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import { createFakeServer } from "@/test/fakeServer";
import { useLibraryItemsStore } from "./libraryItems";

const libA = "10000000-0000-4000-8000-00000000000a";
const libB = "10000000-0000-4000-8000-00000000000b";

function setup() {
  const server = createFakeServer();
  server.cookie = true;
  server.items = [
    { id: "20000000-0000-4000-8000-000000000001", libraryId: libA, kind: "Movie", title: "A1" },
    { id: "20000000-0000-4000-8000-000000000002", libraryId: libB, kind: "Movie", title: "B1" },
  ];
  const app = createApp({ render: () => null });
  app.provide(apiKey, createApiClient({ fetch: server.fetch }));
  app.use(createPinia());
  return { server, store: app.runWithContext(() => useLibraryItemsStore()) };
}

describe("library items store", () => {
  it("goes loading -> success / empty / error", async () => {
    const { server, store } = setup();
    const pending = store.open(libA);
    expect(store.state.status).toBe("loading");
    await pending;
    expect(store.state.status).toBe("success");
    expect(store.items.map((item) => item.title)).toEqual(["A1"]);

    await store.open("10000000-0000-4000-8000-00000000000c");
    expect(store.state.status).toBe("empty");

    server.failItems = true;
    await store.open(libB);
    expect(store.state.status === "error" && store.state.error.code).toBe("not_ready");
    server.failItems = false;
    await store.open(libB);
    expect(store.items.map((item) => item.title)).toEqual(["B1"]);
  });

  it("drops a slower response for a library the user already left", async () => {
    const { server, store } = setup();
    const original = server.fetch;
    let release = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let first = true;
    server.fetch = async (input, init) => {
      if (first) {
        first = false;
        await gate;
      }
      return original(input, init);
    };
    const slow = store.open(libA);
    await store.open(libB);
    release();
    await slow;
    expect(store.libraryId).toBe(libB);
    expect(store.items.map((item) => item.title)).toEqual(["B1"]);
    expect(store.state.status).toBe("success");
  });
});
