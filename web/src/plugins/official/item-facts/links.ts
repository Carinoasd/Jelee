import type { PluginItem } from "@jelee/plugin-sdk";

export interface ExternalLink {
  readonly label: string;
  readonly href: string;
}

/**
 * Links for well-formed TMDB and IMDb IDs only; anything else is skipped so
 * catalog data can never produce an arbitrary URL.
 */
export function externalLinks(item: PluginItem): ExternalLink[] {
  const links: ExternalLink[] = [];
  for (const id of item.externalIds) {
    const type = id.type.toLowerCase();
    if (type === "tmdb" && /^\d{1,10}$/.test(id.value)) {
      const kind = item.kind === "Series" || item.kind === "Season" || item.kind === "Episode" ? "tv" : "movie";
      links.push({ label: "TMDB", href: `https://www.themoviedb.org/${kind}/${id.value}` });
    } else if (type === "imdb" && /^tt\d{5,10}$/.test(id.value)) {
      links.push({ label: "IMDb", href: `https://www.imdb.com/title/${id.value}/` });
    }
  }
  return links;
}

/** Completeness checklist in display order. */
export function completeness(item: PluginItem): { key: string; present: boolean }[] {
  return [
    { key: "field.originalTitle", present: item.originalTitle !== null && item.originalTitle !== "" },
    { key: "field.year", present: item.productionYear !== null },
    { key: "field.overview", present: item.hasOverview },
    { key: "field.genres", present: item.genres.length > 0 },
    { key: "field.externalIds", present: item.externalIds.length > 0 },
    { key: "field.nfo", present: item.nfoStatus === "valid" },
  ];
}

export function formatSize(bytes: number, locale: string): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return new Intl.NumberFormat(locale, { maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value) + " " + (units[unit] ?? "B");
}
