// Locale set and negotiation shared with the server (G03.1, G03.3):
// zh-CN is the default when the browser states no preference, en-US is the
// silent fallback for unsupported languages, and a signed-in user's saved
// locale overrides the browser.
export const supportedLocales = ["zh-CN", "zh-TW", "ja-JP", "en-US"] as const;
export type Locale = (typeof supportedLocales)[number];

export const defaultLocale: Locale = "zh-CN";
export const fallbackLocale: Locale = "en-US";

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (supportedLocales as readonly string[]).includes(value);
}

function match(tag: string): Locale | null {
  const parts = tag.trim().toLowerCase().split("-");
  switch (parts[0]) {
    case "en":
      return "en-US";
    case "ja":
      return "ja-JP";
    case "zh": {
      const traditional = parts.slice(1).some((part) => ["hant", "tw", "hk", "mo"].includes(part));
      return traditional ? "zh-TW" : "zh-CN";
    }
    default:
      return null;
  }
}

/** Picks the first supported language in the browser's preference order. */
export function negotiateLocale(languages: readonly string[]): Locale {
  if (languages.length === 0) {
    return defaultLocale;
  }
  for (const tag of languages) {
    const locale = match(tag);
    if (locale !== null) {
      return locale;
    }
  }
  return fallbackLocale;
}
