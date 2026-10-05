import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import en from "@/i18n/en-US/collections.json";
import { button, control, toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, createRouteFetch, data, expectNoPlaybackMarkup, noContent, regularUser } from "@/test/routeFetch";
import { moveTarget, type Collection, type CollectionView, type Playlist, type PlaylistView } from "./api";

const text = en.collections;
const libraryId = "10000000-0000-4000-8000-00000000000a";
const collectionId = "70000000-0000-4000-8000-000000000001";
const playlistId = "80000000-0000-4000-8000-000000000001";
const arrival = { id: "20000000-0000-4000-8000-000000000001", libraryId, kind: "Movie" as const, title: "Arrival" };
const contact = { id: "20000000-0000-4000-8000-000000000002", libraryId, kind: "Movie" as const, title: "Contact" };

const collection: Collection = {
  id: collectionId,
  name: "Space Saga",
  overview: "",
  nfoName: "Space Saga",
  itemCount: 2,
  coverItemId: arrival.id,
  createdAt: "2026-10-05T00:00:00Z",
  updatedAt: "2026-10-05T00:00:00Z",
};
const collectionView: CollectionView = {
  collection,
  items: [
    { ...arrival, manual: true, fromNfo: false },
    { ...contact, manual: false, fromNfo: true },
  ],
  truncated: false,
};

const playlist: Playlist = {
  id: playlistId,
  name: "Evening",
  public: false,
  ownerId: regularUser.id,
  ownerName: "Viewer",
  owned: true,
  itemCount: 2,
  coverItemId: arrival.id,
  createdAt: "2026-10-05T00:00:00Z",
  updatedAt: "2026-10-05T00:00:00Z",
};
const entries = [
  { entryId: "90000000-0000-4000-8000-000000000001", item: arrival },
  { entryId: "90000000-0000-4000-8000-000000000002", item: contact },
];
const playlistView: PlaylistView = { playlist, entries };

afterEach(() => {
  unmountAll();
});

describe("playlist order", () => {
  it("moves in front of the neighbor or to the end", () => {
    expect(moveTarget(entries, 0, -1)).toBeUndefined();
    expect(moveTarget(entries, 1, 1)).toBeUndefined();
    expect(moveTarget(entries, 1, -1)).toBe(entries[0]!.entryId);
    expect(moveTarget(entries, 0, 1)).toBeNull();
    const three = [...entries, { entryId: "90000000-0000-4000-8000-000000000003", item: arrival }];
    expect(moveTarget(three, 0, 1)).toBe(three[2]!.entryId);
  });
});

describe("collections", () => {
  it("lists collections and lets an administrator create and sync", async () => {
    const server = createRouteFetch()
      .on("GET", "/api/v1/collections", () => data({ collections: [collection], nextCursor: "" }))
      .on("POST", "/api/v1/collections", () => data({ ...collectionView, collection: { ...collection, id: "70000000-0000-4000-8000-000000000002" } }, 201))
      .on("GET", "/api/v1/collections/:id", () => data(collectionView))
      .on("POST", "/api/v1/collections/nfo-sync", () => data({ created: 3 }));
    const { wrapper, router } = await mountView("/collections", { fetch: server.fetch, user: adminUser });
    expect(wrapper.find("h1").text()).toBe(text.title);
    expect(wrapper.text()).toContain("Space Saga");

    await button(wrapper, text.sync).trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/collections/nfo-sync")).toHaveLength(1);
    expect(toastKeys(wrapper)).toContain("collections.synced");

    await wrapper.find("[data-testid='collection-create']").trigger("submit");
    expect(server.calls("POST", "/api/v1/collections")).toHaveLength(0);
    expect(wrapper.text()).toContain(text.form.nameRequired);
    await control(wrapper, text.form.name).setValue("  Heist Films ");
    await wrapper.find("[data-testid='collection-create']").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/collections")[0]!.body).toEqual({ name: "Heist Films", overview: "", nfoName: null });
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("collection");
    });
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("hides administration from viewers", async () => {
    const server = createRouteFetch().on("GET", "/api/v1/collections/:id", () => data(collectionView));
    const { wrapper } = await mountView("/collections/" + collectionId, { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("h1").text()).toBe("Space Saga");
    expect(wrapper.find("[data-testid='collection-edit']").exists()).toBe(false);
    expect(wrapper.findAll("button").filter((b) => b.text() === text.remove)).toHaveLength(0);
    expect(wrapper.text()).toContain(text.fromNfo);
  });

  it("removes only manual members", async () => {
    const after: CollectionView = { ...collectionView, items: [collectionView.items[1]!] };
    const server = createRouteFetch()
      .on("GET", "/api/v1/collections/:id", () => data(collectionView))
      .on("DELETE", "/api/v1/collections/:id/items/:itemId", () => data(after));
    const { wrapper } = await mountView("/collections/" + collectionId, { fetch: server.fetch, user: adminUser });
    const removes = wrapper.findAll("button").filter((b) => b.text() === text.remove);
    expect(removes).toHaveLength(1);
    await removes[0]!.trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/collections/${collectionId}/items/${arrival.id}`)).toHaveLength(1);
    expect(wrapper.text()).not.toContain("Arrival");
  });

  it("deletes a collection after confirmation", async () => {
    const server = createRouteFetch()
      .on("GET", "/api/v1/collections/:id", () => data(collectionView))
      .on("GET", "/api/v1/collections", () => data({ collections: [], nextCursor: "" }))
      .on("DELETE", "/api/v1/collections/:id", () => noContent());
    const { wrapper, router } = await mountView("/collections/" + collectionId, { fetch: server.fetch, user: adminUser });
    await button(wrapper, text.delete).trigger("click");
    await button(wrapper, text.deleteConfirm).trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", "/api/v1/collections/" + collectionId)).toHaveLength(1);
    expect(router.currentRoute.value.name).toBe("collections");
  });
});

describe("playlists", () => {
  it("creates a public playlist", async () => {
    const server = createRouteFetch()
      .on("GET", "/api/v1/playlists", () => data({ playlists: [{ ...playlist, id: "80000000-0000-4000-8000-000000000009", owned: false, public: true, ownerName: "Ada" }], nextCursor: "" }))
      .on("POST", "/api/v1/playlists", () => data(playlistView, 201))
      .on("GET", "/api/v1/playlists/:id", () => data(playlistView));
    const { wrapper, router } = await mountView("/lists", { fetch: server.fetch, user: regularUser });
    expect(wrapper.text()).toContain("By Ada");
    await control(wrapper, text.lists.form.name).setValue("Evening");
    await control(wrapper, text.lists.form.public).setValue(true);
    await wrapper.find("[data-testid='playlist-create']").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/playlists")[0]!.body).toEqual({ name: "Evening", public: true });
    await vi.waitFor(() => {
      expect(router.currentRoute.value.params.listId).toBe(playlistId);
    });
  });

  it("reorders and removes entries of an own playlist", async () => {
    const swapped: PlaylistView = { playlist, entries: [entries[1]!, entries[0]!] };
    const server = createRouteFetch()
      .on("GET", "/api/v1/playlists/:id", () => data(playlistView))
      .on("POST", "/api/v1/playlists/:id/entries/:entryId/move", () => data(swapped))
      .on("DELETE", "/api/v1/playlists/:id/entries/:entryId", () => data({ playlist, entries: [entries[0]!] }))
      .on("PUT", "/api/v1/playlists/:id", () => data({ ...playlistView, playlist: { ...playlist, name: "Late", public: true } }));
    const { wrapper } = await mountView("/lists/" + playlistId, { fetch: server.fetch, user: regularUser });
    const down = wrapper.find(`[aria-label="${text.lists.moveDown.replace("{title}", "Arrival")}"]`);
    await down.trigger("click");
    await flushPromises();
    expect(server.calls("POST", `/api/v1/playlists/${playlistId}/entries/${entries[0]!.entryId}/move`)[0]!.body).toEqual({ beforeEntryId: null });
    expect(wrapper.findAll("li a").map((a) => a.text())).toEqual(["Contact", "Arrival"]);

    await wrapper.findAll("button").filter((b) => b.text() === text.lists.remove)[0]!.trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/playlists/${playlistId}/entries/${entries[1]!.entryId}`)).toHaveLength(1);

    await control(wrapper, text.lists.form.name).setValue("Late");
    await control(wrapper, text.lists.form.public).setValue(true);
    await wrapper.find("[data-testid='playlist-edit']").trigger("submit");
    await flushPromises();
    expect(server.calls("PUT", "/api/v1/playlists/" + playlistId)[0]!.body).toEqual({ name: "Late", public: true });
    expect(toastKeys(wrapper)).toContain("collections.lists.saved");
  });

  it("shows another user's public playlist read-only", async () => {
    const shared: PlaylistView = { playlist: { ...playlist, owned: false, public: true, ownerName: "Ada" }, entries };
    const server = createRouteFetch().on("GET", "/api/v1/playlists/:id", () => data(shared));
    const { wrapper } = await mountView("/lists/" + playlistId, { fetch: server.fetch, user: regularUser });
    expect(wrapper.find("[data-testid='playlist-edit']").exists()).toBe(false);
    expect(wrapper.findAll("button").filter((b) => b.text() === text.lists.remove)).toHaveLength(0);
    expect(wrapper.text()).toContain("By Ada");
    expectNoPlaybackMarkup(wrapper.html());
  });
});
