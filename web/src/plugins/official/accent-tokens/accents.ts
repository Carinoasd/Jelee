import type { ThemeTokens } from "@jelee/plugin-sdk";

/**
 * Accent presets. Each light primary keeps at least 4.5:1 against white
 * text, and each dark primary against its dark text (WCAG 2.1 AA, G33.6).
 */
export const accents = {
  teal: {
    light: { "color-primary": "#0f766e", "color-primary-text": "#ffffff", "color-focus": "#0f766e", "color-chart": "#0f766e" },
    dark: { "color-primary": "#5eead4", "color-primary-text": "#042f2e", "color-focus": "#99f6e4", "color-chart": "#5eead4" },
  },
  violet: {
    light: { "color-primary": "#6d28d9", "color-primary-text": "#ffffff", "color-focus": "#6d28d9", "color-chart": "#6d28d9" },
    dark: { "color-primary": "#c4b5fd", "color-primary-text": "#1e1036", "color-focus": "#ddd6fe", "color-chart": "#c4b5fd" },
  },
  amber: {
    light: { "color-primary": "#b45309", "color-primary-text": "#ffffff", "color-focus": "#b45309", "color-chart": "#b45309" },
    dark: { "color-primary": "#fcd34d", "color-primary-text": "#2a1a00", "color-focus": "#fde68a", "color-chart": "#fcd34d" },
  },
} as const satisfies Record<string, { light: ThemeTokens; dark: ThemeTokens }>;

export type AccentName = keyof typeof accents;
export const accentNames = Object.keys(accents) as AccentName[];

export function isAccentName(value: string): value is AccentName {
  return (accentNames as string[]).includes(value);
}

/** Rounder corners as an optional extra. */
export const roundedTokens: ThemeTokens = { "radius-sm": "6px", "radius-md": "14px" };
