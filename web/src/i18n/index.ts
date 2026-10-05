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
// Namespaces listed in lazyNamespaces stay out of the initial bundle (G35.4):
// they are split into one chunk per locale and merged into the running i18n
// instances by loadNamespace() (awaited by lazy route loaders, see lazyView in
// router/routes.ts) or loadLazyMessages() (for lazy components inside an
// eager page). The two globs must name the same files. devconsole.json belongs
// to the developer mode API console (G49.4). twoFactor.json is
// loaded by twoFactor.ts with the screens that use it.
export const lazyNamespaces = ["versions", "clients", "shares", "networkRules", "devconsole"] as const;
export type LazyNamespace = (typeof lazyNamespaces)[number];

const files = import.meta.glob<Catalog>(
  ["./*/*.json", "!./*/twoFactor.json", "!./*/versions.json", "!./*/clients.json", "!./*/shares.json", "!./*/networkRules.json", "!./*/devconsole.json"],
  { eager: true, import: "default" },
);
const lazyFiles = import.meta.glob<Catalog>(["./*/versions.json", "./*/clients.json", "./*/shares.json", "./*/networkRules.json", "./*/devconsole.json"], { import: "default" });

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

/** Lazy catalogs already fetched, by file path; new instances start with them. */
const lazyLoaded: Record<string, Catalog> = {};
const lazyPending = new Map<LazyNamespace, Promise<void>>();
interface MergeTarget {
  mergeLocaleMessage(locale: string, message: Catalog): void;
}
/** Running instances, held weakly; a namespace loaded later is merged into each. */
const instances = new Set<WeakRef<MergeTarget>>();

/**
 * Fetches a lazy namespace in all four locales (so switching the language
 * later needs no further request) and merges it into every i18n instance.
 * Concurrent calls share one fetch; a failed fetch may be retried.
 */
export function loadNamespace(namespace: LazyNamespace): Promise<void> {
  let pending = lazyPending.get(namespace);
  if (pending === undefined) {
    const entries = Object.entries(lazyFiles).filter(([path]) => path.endsWith("/" + namespace + ".json"));
    pending = Promise.all(entries.map(async ([path, load]) => [path, await load()] as const)).then((loaded) => {
      for (const [path, catalog] of loaded) {
        lazyLoaded[path] = catalog;
        const locale = path.split("/")[1] ?? "";
        for (const ref of instances) {
          const instance = ref.deref();
          if (instance === undefined) {
            instances.delete(ref);
          } else {
            instance.mergeLocaleMessage(locale, catalog);
          }
        }
      }
    });
    pending.catch(() => lazyPending.delete(namespace));
    lazyPending.set(namespace, pending);
  }
  return pending;
}

export function createAppI18n(locale: Locale = defaultLocale) {
  const i18n = createI18n({
    legacy: false,
    locale,
    fallbackLocale,
    messages: buildMessages({ ...files, ...lazyLoaded }),
    missingWarn: import.meta.env.DEV,
    fallbackWarn: import.meta.env.DEV,
  });
  const install = i18n.install.bind(i18n);
  i18n.install = (app, ...options) => {
    install(app, ...options);
    app.provide(currentLocaleKey, () => i18n.global.locale.value);
  };
  instances.add(new WeakRef<MergeTarget>(i18n.global));
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
