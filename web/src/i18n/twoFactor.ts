import { useI18n } from "vue-i18n";
import { supportedLocales } from "./locales";

// Messages of the two-factor and application password screens (G07.8).
// index.ts leaves them out of the eager catalogs so the entry bundle stays
// within its budget (G35.4); every component showing them calls
// useTwoFactorI18n(), which merges them into the global catalogs once per
// i18n instance. The import keeps them in the lazy chunks of those screens.
const files = import.meta.glob<Record<string, unknown>>("./*/twoFactor.json", { eager: true, import: "default" });
const merged = new WeakSet<object>();

export function useTwoFactorI18n() {
  const i18n = useI18n({ useScope: "global" });
  if (!merged.has(i18n)) {
    for (const locale of supportedLocales) {
      const catalog = files[`./${locale}/twoFactor.json`];
      if (catalog !== undefined) {
        i18n.mergeLocaleMessage(locale, catalog);
      }
    }
    merged.add(i18n);
  }
  return i18n;
}
