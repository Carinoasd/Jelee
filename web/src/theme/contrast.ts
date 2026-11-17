// WCAG 2.2 contrast arithmetic (G33.6): relative luminance of sRGB colors
// and the contrast ratio between two of them. Used by the token tests, so
// every theme preset is checked without a browser or an audit library.

export interface Rgb {
  readonly r: number;
  readonly g: number;
  readonly b: number;
}

/** Parses #rgb or #rrggbb; throws on anything else so a token typo fails loudly. */
export function parseHex(value: string): Rgb {
  const match = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(value.trim());
  if (match === null) {
    throw new TypeError("not a hex color: " + value);
  }
  const digits = match[1] ?? "";
  const hex = digits.length === 3 ? digits.replace(/./g, (digit) => digit + digit) : digits;
  return { r: parseInt(hex.slice(0, 2), 16), g: parseInt(hex.slice(2, 4), 16), b: parseInt(hex.slice(4, 6), 16) };
}

function channel(value: number): number {
  const c = value / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

export function relativeLuminance(color: Rgb): number {
  return 0.2126 * channel(color.r) + 0.7152 * channel(color.g) + 0.0722 * channel(color.b);
}

/** Contrast ratio from 1 to 21, whichever color is lighter. */
export function contrastRatio(a: Rgb, b: Rgb): number {
  const [light, dark] = [relativeLuminance(a), relativeLuminance(b)].sort((x, y) => y - x) as [number, number];
  return (light + 0.05) / (dark + 0.05);
}

/** A translucent color painted over an opaque one. */
export function composite(top: Rgb, alpha: number, bottom: Rgb): Rgb {
  const mix = (x: number, y: number) => Math.round(x * alpha + y * (1 - alpha));
  return { r: mix(top.r, bottom.r), g: mix(top.g, bottom.g), b: mix(top.b, bottom.b) };
}
