import { useI18n } from "vue-i18n";
import { isLocale, type Locale } from "./locales";

/** Applies an explicit locale choice; the user's saved locale wins over the browser. */
export function useLocaleSync() {
  const { locale } = useI18n({ useScope: "global" });
  function setLocale(next: Locale) {
    locale.value = next;
    document.documentElement.lang = next;
  }
  function applyUserLocale(value: unknown) {
    if (isLocale(value)) {
      setLocale(value);
    }
  }
  return { locale, setLocale, applyUserLocale };
}
