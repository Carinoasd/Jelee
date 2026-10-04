import type { InjectionKey } from "vue";
import { createI18n } from "vue-i18n";
import { defaultLocale, fallbackLocale, supportedLocales, type Locale } from "./locales";

interface Catalog {
  [key: string]: string | Catalog;
}

// Each file is web/src/i18n/<locale>/<namespace>.json and holds a single
// top-level key equal to its namespace; scripts/check-i18n.mjs enforces this.
// The file name core.json is reserved (see docs/frontend-adr.md).
//
// Lazy namespaces stay out of the initial bundle (G35.4): the page that needs
// one loads it with loadLazyMessages before rendering its text. The gate
// checks them like every other namespace. twoFactor.json is loaded by
// twoFactor.ts with the screens that use it.
export const lazyNamespaces = ["versions"] as const;
export type LazyNamespace = (typeof lazyNamespaces)[number];
const files = import.meta.glob<Catalog>(["./*/*.json", "!./*/twoFactor.json", "!./*/versions.json"], { eager: true, import: "default" });
const lazyFiles = import.meta.glob<Catalog>("./*/versions.json", { import: "default" });

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

/**
 * Reads the current interface language (reactive). Provided wherever the
 * i18n plugin is installed, for code outside components such as the plugin
 * host store, which must not depend on vue-i18n's internal injection symbol.
 */
export const currentLocaleKey: InjectionKey<() => string> = Symbol("jelee.locale");

export function createAppI18n(locale: Locale = defaultLocale) {
  const i18n = createI18n({
    legacy: false,
    locale,
    fallbackLocale,
    messages: buildMessages(files),
    missingWarn: import.meta.env.DEV,
    fallbackWarn: import.meta.env.DEV,
  });
  const install = i18n.install.bind(i18n);
  i18n.install = (app, ...options) => {
    install(app, ...options);
    app.provide(currentLocaleKey, () => i18n.global.locale.value);
  };
  return i18n;
}

export type AppI18n = ReturnType<typeof createAppI18n>;

/** Something that accepts messages for a locale, such as the global composer. */
export interface MessageTarget {
  mergeLocaleMessage(locale: string, messages: Catalog): void;
}

const loadedNamespaces = new WeakMap<MessageTarget, Set<string>>();

/**
 * Loads a lazy namespace for every locale into target, once per target, so
 * a later language switch finds the text as well.
 */
export async function loadLazyMessages(target: MessageTarget, namespace: LazyNamespace): Promise<void> {
  const loaded = loadedNamespaces.get(target) ?? new Set<string>();
  loadedNamespaces.set(target, loaded);
  if (loaded.has(namespace)) {
    return;
  }
  const entries = Object.entries(lazyFiles).filter(([path]) => path.endsWith("/" + namespace + ".json"));
  const catalogs = await Promise.all(entries.map(async ([path, load]) => [path, await load()] as const));
  for (const [locale, messages] of Object.entries(buildMessages(Object.fromEntries(catalogs)))) {
    target.mergeLocaleMessage(locale, messages);
  }
  loaded.add(namespace);
}
