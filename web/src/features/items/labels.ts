import type { ItemKind } from "./api";

/** Catalog keys for item kinds; full literals so the i18n gate can see them. */
export const kindLabelKey: Readonly<Record<ItemKind, string>> = {
  Movie: "items.kind.movie",
  HomeVideo: "items.kind.homeVideo",
  Series: "items.kind.series",
  Season: "items.kind.season",
  Episode: "items.kind.episode",
};

export const nfoStatusKey = {
  valid: "items.detail.nfoValid",
  missing: "items.detail.nfoMissing",
  nfo_invalid: "items.detail.nfoInvalid",
} as const;

export type ItemView = "poster" | "list";

/** Reads the ?view= query value; the poster wall is the default. */
export function parseItemView(value: unknown): ItemView {
  return value === "list" ? "list" : "poster";
}
