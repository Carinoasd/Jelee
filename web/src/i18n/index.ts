import { createI18n } from "vue-i18n";
import { defaultLocale, fallbackLocale, supportedLocales, type Locale } from "./locales";

interface Catalog {
  [key: string]: string | Catalog;
}

// Each file is web/src/i18n/<locale>/<namespace>.json and holds a single
// top-level key equal to its namespace; scripts/check-i18n.mjs enforces this.
// The file name core.json is reserved (see docs/frontend-adr.md).
const files = import.meta.glob<Catalog>("./*/*.json", { eager: true, import: "default" });

export function buildMessages(source: Record<string, Catalog>): Record<Locale, Catalog> {
  const messages = Object.fromEntries(supportedLocales.map((locale) => [locale, {}])) as Record<Locale, Catalog>;
  for (const [path, catalog] of Object.entries(source)) {
    const [, locale, file] = path.split("/");
    const target = supportedLocales.find((candidate) => candidate === locale);
    if (target === undefined) {
      continue;
    }
    // core.json is reserved for the server-side UI strings and may be flat;
    // every other file already wraps its messages in its namespace key.
    const wrapped = file === "core.json" && !("core" in catalog) ? { core: catalog } : catalog;
    Object.assign(messages[target], wrapped);
  }
  return messages;
}

export function createAppI18n(locale: Locale = defaultLocale) {
  return createI18n({
    legacy: false,
    locale,
    fallbackLocale,
    messages: buildMessages(files),
    missingWarn: import.meta.env.DEV,
    fallbackWarn: import.meta.env.DEV,
  });
}

export type AppI18n = ReturnType<typeof createAppI18n>;
