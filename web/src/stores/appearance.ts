import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { defaultCssPolicy, fontHostLimit, normalizeFontHost, sanitizeCustomCss, type CssPolicy, type SanitizedCss } from "@/theme/customCss";
import { applyStyleLayer } from "@/theme/styleSheets";
import { readPersisted, removePersisted, writePersisted } from "./persist";

// Administrator CSS (G33.4). The input is sanitized whenever it is applied,
// including after reading it back from storage, and applied as a constructed
// stylesheet so the CSP needs no 'unsafe-inline'. There is no server API for
// global appearance settings yet: the CSS lives in this browser only (see
// docs/frontend-adr.md for the gap and the CSP font-src note).

const storageKey = "admin-css.custom";

export interface AppearanceSettings {
  readonly css: string;
  readonly policy: CssPolicy;
}

function readSettings(): AppearanceSettings {
  const raw = readPersisted(storageKey);
  const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const hosts = Array.isArray(record.fontHosts) ? (record.fontHosts as unknown[]) : [];
  return {
    css: typeof record.css === "string" ? record.css : "",
    policy: {
      allowExternalFonts: record.allowExternalFonts === true,
      fontHosts: hosts
        .map((host) => (typeof host === "string" ? normalizeFontHost(host) : null))
        .filter((host): host is string => host !== null)
        .slice(0, fontHostLimit),
    },
  };
}

export const useAppearanceStore = defineStore("appearance", () => {
  const settings = shallowRef<AppearanceSettings>(readSettings());
  const result = shallowRef<SanitizedCss>({ css: "", issues: [], rejected: false });
  /** False when the browser cannot apply constructed stylesheets. */
  const supported = shallowRef(true);

  function apply(): void {
    result.value = sanitizeCustomCss(settings.value.css, settings.value.policy);
    supported.value = applyStyleLayer("custom-css", result.value.css) || result.value.css === "";
  }

  /** Checks CSS without applying or storing it. */
  function check(css: string, policy: CssPolicy): SanitizedCss {
    return sanitizeCustomCss(css, policy);
  }

  /** Applies and stores new settings; returns the sanitizer's verdict. */
  function save(css: string, policy: CssPolicy): SanitizedCss {
    settings.value = { css, policy: { allowExternalFonts: policy.allowExternalFonts, fontHosts: policy.fontHosts.slice(0, fontHostLimit) } };
    writePersisted(storageKey, { css, allowExternalFonts: policy.allowExternalFonts, fontHosts: settings.value.policy.fontHosts });
    apply();
    return result.value;
  }

  function clear(): void {
    settings.value = { css: "", policy: defaultCssPolicy };
    removePersisted(storageKey);
    apply();
  }

  apply();
  return { settings, result, supported, check, save, clear };
});
