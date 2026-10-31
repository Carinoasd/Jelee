import { describe, expect, it } from "vitest";
import { themeTokenNames } from "@jelee/plugin-sdk";
import { composite, contrastRatio, parseHex } from "./contrast";
import { baseTokens, colorTokenNames, extraPaletteIds, paletteIds, palettes, renderTokenCss, type ColorTokenName, type PaletteId } from "./tokens";

// WCAG 2.2 AA (G33.6): 4.5:1 for text, 3:1 for focus indicators (1.4.11).
const textPairs: readonly [ColorTokenName, ColorTokenName][] = [
  ["color-text", "color-bg"],
  ["color-text", "color-surface"],
  ["color-text", "color-badge-bg"],
  ["color-text", "color-info-bg"],
  ["color-text", "color-danger-bg"],
  ["color-text", "color-success-bg"],
  ["color-text-muted", "color-bg"],
  ["color-text-muted", "color-surface"],
  ["color-text-muted", "color-badge-bg"],
  ["color-primary", "color-bg"],
  ["color-primary", "color-surface"],
  ["color-primary-text", "color-primary"],
  ["color-danger", "color-surface"],
  ["color-danger", "color-danger-bg"],
  ["color-success", "color-surface"],
  ["color-success", "color-success-bg"],
];
const focusPairs: readonly [ColorTokenName, ColorTokenName][] = [
  ["color-focus", "color-bg"],
  ["color-focus", "color-surface"],
];

function ratio(palette: PaletteId, foreground: ColorTokenName, background: ColorTokenName): number {
  const colors = palettes[palette].colors;
  return contrastRatio(parseHex(colors[foreground]), parseHex(colors[background]));
}

describe("contrast arithmetic", () => {
  it("matches the WCAG reference values", () => {
    expect(contrastRatio(parseHex("#000"), parseHex("#fff"))).toBeCloseTo(21, 5);
    expect(contrastRatio(parseHex("#777777"), parseHex("#ffffff"))).toBeCloseTo(4.48, 2);
    expect(contrastRatio(parseHex("#ffffff"), parseHex("#ffffff"))).toBe(1);
    expect(() => parseHex("red")).toThrow(TypeError);
  });
});

describe.each(paletteIds)("theme %s", (palette) => {
  it.each(textPairs)("%s on %s reaches 4.5:1", (foreground, background) => {
    expect(ratio(palette, foreground, background)).toBeGreaterThanOrEqual(4.5);
  });

  it.each(focusPairs)("%s on %s reaches 3:1", (foreground, background) => {
    expect(ratio(palette, foreground, background)).toBeGreaterThanOrEqual(3);
  });

  it("defines every color token with a hex value", () => {
    expect(Object.keys(palettes[palette].colors).sort()).toEqual([...colorTokenNames].sort());
  });
});

describe("design tokens", () => {
  it("keeps hover text readable over the poster overlay on any image", () => {
    const overlay = /^rgb\(0 0 0 \/ (\d+)%\)$/.exec(baseTokens["color-overlay"] ?? "");
    expect(overlay).not.toBeNull();
    const alpha = Number(overlay![1]) / 100;
    // The worst case is a white poster under the overlay.
    const backdrop = composite(parseHex("#000000"), alpha, parseHex("#ffffff"));
    expect(contrastRatio(parseHex(baseTokens["color-overlay-text"]!), backdrop)).toBeGreaterThanOrEqual(4.5);
  });

  it("compiles every preset and choice into the stylesheet", () => {
    const css = renderTokenCss();
    for (const id of extraPaletteIds) {
      expect(css).toContain(`:root[data-theme][data-palette="${id}"]`);
    }
    for (const selector of [':root[data-density="compact"]', ':root[data-poster="landscape"]', ':root[data-poster-size="large"]', ':root[data-list-density="compact"]']) {
      expect(css).toContain(selector);
    }
    expect(css).toContain("@media (prefers-reduced-motion: reduce)");
  });

  it("defines every token the components and plugins use", () => {
    const defined = new Set([...Object.keys(baseTokens), ...colorTokenNames]);
    const sources = import.meta.glob<string>("/src/**/*.{vue,css}", { query: "?raw", import: "default", eager: true });
    const used = new Set<string>();
    for (const text of Object.values(sources)) {
      for (const match of text.matchAll(/var\(--jl-([a-z0-9-]+)/g)) {
        used.add(match[1]!);
      }
    }
    expect(used.size).toBeGreaterThan(20);
    expect([...used].filter((name) => !defined.has(name))).toEqual([]);
    expect(themeTokenNames.filter((name) => !defined.has(name))).toEqual([]);
  });
});
