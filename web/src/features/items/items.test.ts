import { describe, expect, it } from "vitest";
import { createApiClient } from "@/api/client";
import { createFakeServer } from "@/test/fakeServer";
import { listLibraryItems, libraryItemsTarget, maxPagesPerLoad, posterUrl, type ItemMetadata } from "./api";
import { parseItemView } from "./labels";
import { summarizeMetadata } from "./metadata";

const lib = "10000000-0000-4000-8000-00000000000a";
const other = "10000000-0000-4000-8000-00000000000b";

function uuid(n: number) {
  return "20000000-0000-4000-8000-" + String(n).padStart(12, "0");
}

describe("listLibraryItems", () => {
  it("keeps top-level items of the library and walks pages until enough are found", async () => {
    const server = createFakeServer();
    server.cookie = true;
    server.pageSize = 100;
    server.items = Array.from({ length: 250 }, (_, n) => ({
      id: uuid(n),
      libraryId: n % 5 === 0 ? lib : other,
      kind: "Movie" as const,
      title: `Item ${n}`,
      ...(n === 5 ? { parentId: uuid(0) } : {}),
    }));
    const { client } = createApiClient({ fetch: server.fetch });
    const page = await listLibraryItems(client, lib);
    // 50 matching items exist (one is a child); fewer than the target, so the
    // whole catalog is read and the cursor ends.
    expect(page.items).toHaveLength(49);
    expect(page.items.every((item) => item.libraryId === lib && item.parentId === undefined)).toBe(true);
    expect(page.nextCursor).toBe("");
    expect(server.requests).toHaveLength(3);
    expect(libraryItemsTarget).toBeGreaterThan(0);
  });

  it("stops after the page budget and returns a cursor to continue", async () => {
    const server = createFakeServer();
    server.cookie = true;
    server.pageSize = 100;
    server.items = Array.from({ length: (maxPagesPerLoad + 2) * 100 }, (_, n) => ({
      id: uuid(n),
      libraryId: other,
      kind: "Movie" as const,
      title: `Item ${n}`,
    }));
    const { client } = createApiClient({ fetch: server.fetch });
    const page = await listLibraryItems(client, lib);
    expect(page.items).toEqual([]);
    expect(server.requests).toHaveLength(maxPagesPerLoad);
    expect(page.nextCursor).not.toBe("");
    const rest = await listLibraryItems(client, lib, page.nextCursor);
    expect(rest.nextCursor).toBe("");
  });
});

describe("item helpers", () => {
  it("builds same-origin artwork URLs and parses the view", () => {
    expect(posterUrl("a/b", 300)).toBe("/images/Primary/a%2Fb?width=300");
    expect(parseItemView("list")).toBe("list");
    expect(parseItemView(["list"])).toBe("poster");
    expect(parseItemView(undefined)).toBe("poster");
  });

  it("summarizes metadata defensively", () => {
    const base = { locked: false, updatedAt: null, nfoOrigin: null, nfoLockOrigin: null };
    const summary = summarizeMetadata({
      itemId: uuid(1),
      libraryId: lib,
      kind: "Movie",
      revision: 1,
      fields: [
        { ...base, field: "overview", value: "  Text  ", source: "nfo", providerOrigin: null },
        { ...base, field: "date", value: "1999-03-31", source: "manual", providerOrigin: null },
        { ...base, field: "tagline", value: "", source: "nfo", providerOrigin: null },
      ],
      facts: [
        { ...base, field: "uniqueIds", source: "nfo", value: [{ type: "tmdb", value: "603", default: true }, { bogus: 1 }, "x"] },
        { ...base, field: "genres", source: "manual", value: ["Action", 3, ""] },
      ],
      lastConfirmedNFOObservation: null,
    } as unknown as ItemMetadata);
    expect(summary.overview).toBe("Text");
    expect(summary.year).toBe(1999);
    expect(summary.tagline).toBe("");
    expect(summary.externalIds).toEqual([{ type: "tmdb", value: "603", isDefault: true }]);
    expect(summary.genres).toEqual(["Action"]);
    expect([...summary.nfoFields].sort()).toEqual(["overview", "uniqueIds"]);
    expect(summary.nfoStatus).toBeNull();
  });

  it("prefers the year fact and reports the NFO observation", () => {
    const summary = summarizeMetadata({
      itemId: uuid(1),
      libraryId: lib,
      kind: "Movie",
      revision: 1,
      fields: [],
      facts: [{ field: "year", value: 2001, source: "existing", locked: false, updatedAt: null, nfoOrigin: null, nfoLockOrigin: null }],
      lastConfirmedNFOObservation: { status: "valid", readAt: "2026-10-01T00:00:00Z" },
    } as unknown as ItemMetadata);
    expect(summary.year).toBe(2001);
    expect(summary.nfoStatus).toBe("valid");
    expect(summary.nfoReadAt).toBe("2026-10-01T00:00:00Z");
  });
});
