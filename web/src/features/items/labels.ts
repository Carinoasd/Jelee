import type { ItemKind, LibrarySort } from "./api";

/** Catalog keys for item kinds; full literals so the i18n gate can see them. */
export const kindLabelKey: Readonly<Record<ItemKind, string>> = {
  Movie: "items.kind.movie",
  HomeVideo: "items.kind.homeVideo",
  Series: "items.kind.series",
  Season: "items.kind.season",
  Episode: "items.kind.episode",
};

export const nfoStatusKey = {
  unread: "items.detail.nfoNone",
  valid: "items.detail.nfoValid",
  missing: "items.detail.nfoMissing",
  nfo_invalid: "items.detail.nfoInvalid",
} as const;

export const sortLabelKey: Readonly<Record<LibrarySort, string>> = {
  name: "items.sort.name",
  newest: "items.sort.newest",
  year: "items.sort.year",
};

export type ItemView = "poster" | "list";

/** Reads the ?view= query value; the poster wall is the default. */
export function parseItemView(value: unknown): ItemView {
  return value === "list" ? "list" : "poster";
}

/** Reads the ?sort= query value; name order is the default. */
export function parseLibrarySort(value: unknown): LibrarySort {
  return value === "newest" || value === "year" ? value : "name";
}
