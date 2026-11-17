// Design tokens: the single source for color, spacing, radius, shadow, type,
// layers and motion (G33.1, G34.1). vite.config.ts compiles this module into
// the bundled stylesheet (virtual:jelee-tokens.css), so the page needs no
// runtime script or inline style for its tokens and the CSP stays
// style-src 'self'. Components only read the resulting --jl-* custom
// properties; runtime overrides (plugin tokens, site tokens, administrator
// CSS) replace them through constructed stylesheets (theme/styleSheets.ts).
//
// The root element carries the user's choices (stores/preferences.ts):
// data-theme (light or dark, absent for the system scheme), data-palette
// (a preset beyond plain light or dark), data-density, data-poster,
// data-poster-size and data-list-density. Spacing follows a 4/8 px grid in
// both densities. This module imports nothing, so the build config can load
// it as well as the application and the contrast tests.

export const colorTokenNames = [
  "color-bg",
  "color-surface",
  "color-text",
  "color-text-muted",
  "color-border",
  "color-primary",
  "color-primary-text",
  "color-danger",
  "color-danger-bg",
  "color-focus",
  "color-success",
  "color-success-bg",
  "color-info-bg",
  "color-skeleton",
  "color-skeleton-highlight",
  "color-badge-bg",
] as const;
export type ColorTokenName = (typeof colorTokenNames)[number];
export type ColorScheme = "light" | "dark";

export interface Palette {
  /** The scheme the palette belongs to: data-theme and color-scheme follow it. */
  readonly scheme: ColorScheme;
  readonly colors: Readonly<Record<ColorTokenName, string>>;
}

const light: Palette = {
  scheme: "light",
  colors: {
    "color-bg": "#f7f7f9",
    "color-surface": "#ffffff",
    "color-text": "#1b1d22",
    "color-text-muted": "#545a66",
    "color-border": "#c9cdd6",
    "color-primary": "#2f5bd3",
    "color-primary-text": "#ffffff",
    "color-danger": "#b42318",
    "color-danger-bg": "#fdecea",
    "color-focus": "#1d4ed8",
    "color-success": "#146c2e",
    "color-success-bg": "#e6f4ea",
    "color-info-bg": "#eef2fc",
    "color-skeleton": "#e3e6ec",
    "color-skeleton-highlight": "#f1f3f6",
    "color-badge-bg": "#e9ecf2",
  },
};

const dark: Palette = {
  scheme: "dark",
  colors: {
    "color-bg": "#121418",
    "color-surface": "#1c1f26",
    "color-text": "#eceef2",
    "color-text-muted": "#a9afbb",
    "color-border": "#3a3f4b",
    "color-primary": "#7ea2ff",
    "color-primary-text": "#0b1530",
    "color-danger": "#ffb4ab",
    "color-danger-bg": "#3b1512",
    "color-focus": "#a8c1ff",
    "color-success": "#8fd9a0",
    "color-success-bg": "#12301b",
    "color-info-bg": "#1a2338",
    "color-skeleton": "#262a33",
    "color-skeleton-highlight": "#303541",
    "color-badge-bg": "#2a2f3a",
  },
};

/** Warm paper tones for long reading sessions. */
const sepia: Palette = {
  scheme: "light",
  colors: {
    "color-bg": "#f4ecd8",
    "color-surface": "#fbf6ea",
    "color-text": "#2b2218",
    "color-text-muted": "#5c4d3c",
    "color-border": "#cbbb9c",
    "color-primary": "#8a4b14",
    "color-primary-text": "#ffffff",
    "color-danger": "#a1251b",
    "color-danger-bg": "#f8e1d8",
    "color-focus": "#7a3e0c",
    "color-success": "#2f6b25",
    "color-success-bg": "#e3eed3",
    "color-info-bg": "#efe5cf",
    "color-skeleton": "#e6dcc4",
    "color-skeleton-highlight": "#efe7d4",
    "color-badge-bg": "#e9dec4",
  },
};

/** High contrast on black (AAA for body text). */
const contrast: Palette = {
  scheme: "dark",
  colors: {
    "color-bg": "#000000",
    "color-surface": "#0d0d0d",
    "color-text": "#ffffff",
    "color-text-muted": "#d6d6d6",
    "color-border": "#9a9a9a",
    "color-primary": "#ffd84a",
    "color-primary-text": "#000000",
    "color-danger": "#ff9b8f",
    "color-danger-bg": "#3a0d08",
    "color-focus": "#ffffff",
    "color-success": "#7dff9a",
    "color-success-bg": "#0a2a12",
    "color-info-bg": "#101a2e",
    "color-skeleton": "#1f1f1f",
    "color-skeleton-highlight": "#2c2c2c",
    "color-badge-bg": "#262626",
  },
};

/** Theme presets; light and dark are the base palettes the system scheme picks between. */
export const palettes = { light, dark, sepia, contrast } as const satisfies Readonly<Record<string, Palette>>;
export type PaletteId = keyof typeof palettes;
export const paletteIds = Object.keys(palettes) as PaletteId[];
/** Presets applied with data-palette on top of their scheme. */
export const extraPaletteIds = paletteIds.filter((id) => id !== "light" && id !== "dark");

/** Row padding and thumbnail width of list views. */
export const listDensities = { comfortable: { "list-row-padding": "8px", "list-thumb-width": "48px" }, compact: { "list-row-padding": "4px", "list-thumb-width": "32px" } } as const;
export type ListDensity = keyof typeof listDensities;

/** Tokens that do not depend on the palette. */
export const baseTokens: Readonly<Record<string, string>> = {
  "color-overlay": "rgb(0 0 0 / 70%)",
  "color-overlay-text": "#ffffff",
  "color-chart": "var(--jl-color-primary)",
  "color-chart-track": "var(--jl-color-badge-bg)",
  "space-1": "4px",
  "space-2": "8px",
  "space-3": "12px",
  "space-4": "16px",
  "space-6": "24px",
  "space-8": "32px",
  "space-12": "48px",
  "radius-sm": "4px",
  "radius-md": "8px",
  "radius-pill": "999px",
  "shadow-1": "0 1px 2px rgb(0 0 0 / 8%), 0 1px 3px rgb(0 0 0 / 6%)",
  "shadow-2": "0 4px 12px rgb(0 0 0 / 14%), 0 2px 4px rgb(0 0 0 / 8%)",
  "font-family":
    'system-ui, -apple-system, "Segoe UI", "Noto Sans", "Hiragino Sans", "Microsoft JhengHei", "Microsoft YaHei", sans-serif',
  "font-size-sm": "0.875rem",
  "font-size-md": "1rem",
  "font-size-lg": "1.25rem",
  "font-size-xl": "1.5rem",
  "z-header": "10",
  "z-toast": "50",
  "motion-duration": "200ms",
  "motion-easing": "cubic-bezier(0.2, 0, 0, 1)",
  "touch-target": "44px",
  "poster-ratio": "2 / 3",
  "poster-min-width": "150px",
  ...listDensities.comfortable,
  "content-max-width": "1200px",
};

export const densities = ["comfortable", "compact"] as const;
export type Density = (typeof densities)[number];
/** The compact density tightens spacing on the same 4/8 px grid; touch targets stay 44 px. */
const compactSpacing: Readonly<Record<string, string>> = {
  "space-2": "4px",
  "space-3": "8px",
  "space-4": "12px",
  "space-6": "16px",
  "space-8": "24px",
  "space-12": "32px",
};

export const posterRatios = { portrait: "2 / 3", square: "1 / 1", landscape: "16 / 9" } as const;
export type PosterRatio = keyof typeof posterRatios;
export const posterSizes = { small: "120px", medium: "150px", large: "200px" } as const;
export type PosterSize = keyof typeof posterSizes;

function block(selector: string, tokens: Readonly<Record<string, string>>, extra: readonly string[] = []): string {
  const lines = Object.entries(tokens).map(([name, value]) => `  --jl-${name}: ${value};`);
  return `${selector} {\n${[...lines, ...extra.map((line) => `  ${line}`)].join("\n")}\n}`;
}

/** Compiles the tokens into the stylesheet the bundle ships. */
export function renderTokenCss(): string {
  const parts = [
    "/* Generated from web/src/theme/tokens.ts; do not edit. */",
    block(":root", { ...baseTokens, ...light.colors }, ["color-scheme: light dark;"]),
    // Dark follows the system unless the user picked a theme (data-theme).
    `@media (prefers-color-scheme: dark) {\n${block(':root:not([data-theme="light"])', dark.colors)}\n}`,
    block(':root[data-theme="dark"]', dark.colors, ["color-scheme: dark;"]),
    block(':root[data-theme="light"]', {}, ["color-scheme: light;"]),
    // Presets outrank the site's light and dark token overrides (three
    // attribute selectors against two), which are written for the base palettes.
    ...extraPaletteIds.map((id) =>
      block(`:root[data-theme][data-palette="${id}"]`, palettes[id].colors, [`color-scheme: ${palettes[id].scheme};`]),
    ),
    block(':root[data-density="compact"]', compactSpacing),
    ...(Object.keys(posterRatios) as PosterRatio[])
      .filter((id) => id !== "portrait")
      .map((id) => block(`:root[data-poster="${id}"]`, { "poster-ratio": posterRatios[id] })),
    ...(Object.keys(posterSizes) as PosterSize[])
      .filter((id) => id !== "medium")
      .map((id) => block(`:root[data-poster-size="${id}"]`, { "poster-min-width": posterSizes[id] })),
    block(':root[data-list-density="compact"]', listDensities.compact),
    `@media (prefers-reduced-motion: reduce) {\n${block(":root", { "motion-duration": "0ms" })}\n}`,
  ];
  return parts.join("\n\n") + "\n";
}
