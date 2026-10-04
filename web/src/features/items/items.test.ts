import { describe, expect, it } from "vitest";
import { createApiClient } from "@/api/client";
import { createFakeServer } from "@/test/fakeServer";
import { getItemDetails, getItemSources, libraryItemsPageSize, listLibraryItems, posterUrl, type MediaSourceInfo } from "./api";
import { codecsOf, formatBitrate, formatBytes, formatDuration, resolutionOf } from "./files";
import { parseItemView, parseLibrarySort } from "./labels";

const lib = "10000000-0000-4000-8000-00000000000a";
const other = "10000000-0000-4000-8000-00000000000b";

function uuid(n: number) {
  return "20000000-0000-4000-8000-" + String(n).padStart(12, "0");
}

describe("listLibraryItems", () => {
  it("asks the server for one sorted page of the library's top level", async () => {
    const server = createFakeServer();
    server.cookie = true;
    server.pageSize = 100;
    server.items = Array.from({ length: 250 }, (_, n) => ({
      id: uuid(n),
      libraryId: n % 2 === 0 ? lib : other,
      kind: "Movie" as const,
      title: `Item ${String(n).padStart(3, "0")}`,
      productionYear: 1900 + n,
      ...(n === 4 ? { parentId: uuid(0) } : {}),
    }));
    const { client } = createApiClient({ fetch: server.fetch });
    const page = await listLibraryItems(client, lib);
    expect(page.total).toBe(124);
    expect(page.items).toHaveLength(libraryItemsPageSize);
    expect(page.items[0]!.title).toBe("Item 000");
    // One request per page: no client-side walk over the global catalog.
    expect(server.requests).toHaveLength(1);
    const query = new URL(server.requests[0]!.url).searchParams;
    expect(Object.fromEntries(query)).toEqual({ parentId: lib, sort: "name", order: "asc", offset: "0", limit: String(libraryItemsPageSize) });

    const last = await listLibraryItems(client, lib, 120, "year");
    expect(last.items.map((item) => item.productionYear)).toEqual([1908, 1906, 1902, 1900]);
    const second = new URL(server.requests[1]!.url).searchParams;
    expect(second.get("sort")).toBe("productionYear");
    expect(second.get("order")).toBe("desc");
    expect(second.get("offset")).toBe("120");
  });
});

describe("item details and file information", () => {
  it("reads the public endpoints", async () => {
    const server = createFakeServer();
    server.cookie = true;
    server.items = [{ id: uuid(1), libraryId: lib, kind: "Movie", title: "Arrival" }];
    server.details[uuid(1)] = { overview: "Text", genres: ["Drama"] };
    const { client } = createApiClient({ fetch: server.fetch });
    const details = await getItemDetails(client, uuid(1));
    expect(details.overview).toBe("Text");
    expect(details.nfo.status).toBe("unread");
    expect(await getItemSources(client, uuid(1))).toEqual([]);
    expect(server.requests.map((request) => new URL(request.url).pathname)).toEqual([
      `/api/v1/items/${uuid(1)}/details`,
      `/api/v1/items/${uuid(1)}/sources`,
    ]);
  });
});

describe("item helpers", () => {
  it("builds same-origin artwork URLs and parses the view and order", () => {
    expect(posterUrl("a/b", 300)).toBe("/images/Primary/a%2Fb?width=300");
    expect(parseItemView("list")).toBe("list");
    expect(parseItemView(["list"])).toBe("poster");
    expect(parseItemView(undefined)).toBe("poster");
    expect(parseLibrarySort("year")).toBe("year");
    expect(parseLibrarySort("newest")).toBe("newest");
    expect(parseLibrarySort("name; drop")).toBe("name");
    expect(parseLibrarySort(["year"])).toBe("name");
  });

  it("formats file facts and skips unknown values", () => {
    expect(formatDuration(7_265_000_000)).toBe("2:01:05");
    expect(formatDuration(65_400_000)).toBe("1:05");
    expect(formatDuration(undefined)).toBe("");
    expect(formatDuration(-1)).toBe("");
    expect(formatBytes(9_000_000_000, "en-US")).toBe("8.4 GB");
    expect(formatBytes(512, "en-US")).toBe("512 byte");
    expect(formatBytes(undefined, "en-US")).toBe("");
    expect(formatBitrate(10_344_827, "en-US")).toBe("10.3 Mb/s");
    expect(formatBitrate(320_000, "en-US")).toBe("320 kb/s");
    expect(formatBitrate(0, "en-US")).toBe("");
    const source = {
      videoTracks: [
        { index: 0, width: 640, height: 360, default: false, primary: false, codec: "mjpeg" },
        { index: 1, width: 3840, height: 2160, default: true, primary: true, codec: "hevc" },
      ],
      audioTracks: [
        { index: 2, codec: "truehd", default: true, forced: false, atmos: true },
        { index: 3, codec: "truehd", default: false, forced: false, atmos: false },
        { index: 4, codec: "ac3", default: false, forced: false, atmos: false },
      ],
    } as unknown as MediaSourceInfo;
    expect(resolutionOf(source)).toBe("3840×2160");
    expect(resolutionOf({ videoTracks: [] } as unknown as MediaSourceInfo)).toBe("");
    expect(codecsOf(source.audioTracks)).toBe("truehd, ac3");
    expect(codecsOf([{}, { codec: "" }])).toBe("");
  });
});
