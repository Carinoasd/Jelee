import { themeTokenNames } from "@jelee/plugin-sdk";
import { defineStore } from "pinia";
import { computed, shallowRef, watch } from "vue";
import { useApi } from "@/api";
import {
  appearanceInput,
  appearanceView,
  getSiteAppearanceConfig,
  resetSiteAppearance,
  saveSiteAppearance,
  type SiteAppearanceConfig,
  type SiteAppearanceInput,
} from "@/features/site/api";
import {
  defaultCssPolicy,
  fontHostLimit,
  normalizeFontHost,
  sanitizeCustomCss,
  tokenStyleSheet,
  type CssPolicy,
  type SanitizedCss,
} from "@/theme/customCss";
import { applyStyleLayer } from "@/theme/styleSheets";
import { useAuthStore } from "./auth";
import { readPersisted, removePersisted, writePersisted } from "./persist";
import { useSiteAppearanceStore } from "./siteAppearance";

// Site appearance (G33.2–G33.5): administrator CSS and design token
// overrides, applied as constructed stylesheets so the CSP needs no
// 'unsafe-inline'. With a signed-in user the server's settings apply to
// everyone (GET /api/v1/site/appearance, already sanitized by the server and
// sanitized again here), and administrators edit the stored document with
// its revision (GET/PUT /api/v1/site/appearance[/config]). Signed out, or when
// the server cannot be read, the browser copy applies as before: CSS an
// administrator saved in this browser (admin-css.custom), sanitized on every
// load. The server CSP adds font-src for the allowlisted font hosts.

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

const tokenNames: ReadonlySet<string> = new Set<string>(themeTokenNames);

/** Fields of the site appearance document an administrator edits besides the CSS. */
export type AppearanceDocument = Omit<SiteAppearanceInput, "revision">;

export const useAppearanceStore = defineStore("appearance", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const site = useSiteAppearanceStore();
  const local = shallowRef<AppearanceSettings>(readSettings());
  /** The stored document for administrators; null otherwise or when unreadable. */
  const config = shallowRef<SiteAppearanceConfig | null>(null);
  const result = shallowRef<SanitizedCss>({ css: "", issues: [], rejected: false });
  /** False when the browser cannot apply constructed stylesheets. */
  const supported = shallowRef(true);
  /** Where the applied settings come from. */
  const source = computed<"server" | "browser">(() => (site.view !== null ? "server" : "browser"));

  /** What the editor shows: the stored document, the effective view or the browser copy. */
  const settings = computed<AppearanceSettings>(() => {
    if (config.value !== null) {
      return { css: config.value.customCss, policy: { allowExternalFonts: config.value.allowExternalFonts, fontHosts: config.value.fontHosts } };
    }
    return local.value;
  });

  function apply(): void {
    const view = site.view;
    if (view !== null) {
      // The server sanitized this already; the browser does not trust it either.
      result.value = sanitizeCustomCss(view.css, { allowExternalFonts: view.fontHosts.length > 0, fontHosts: view.fontHosts });
      const tokens = tokenStyleSheet(view.tokens.light, view.tokens.dark, tokenNames);
      const applied = applyStyleLayer("site-tokens", tokens) || tokens === "";
      supported.value = (applyStyleLayer("custom-css", result.value.css) || result.value.css === "") && applied;
      return;
    }
    result.value = sanitizeCustomCss(local.value.css, local.value.policy);
    applyStyleLayer("site-tokens", "");
    supported.value = applyStyleLayer("custom-css", result.value.css) || result.value.css === "";
  }

  let generation = 0;
  /** Reads the stored document for an administrator; others have none. */
  async function loadConfig(): Promise<void> {
    const current = ++generation;
    if (auth.user?.admin !== true) {
      config.value = null;
      return;
    }
    try {
      const next = await getSiteAppearanceConfig(client);
      if (current === generation) {
        config.value = next;
      }
    } catch {
      if (current === generation) {
        config.value = null;
      }
    }
  }

  function stored(next: SiteAppearanceConfig): SanitizedCss {
    generation++;
    config.value = next;
    const verdict = sanitizeCustomCss(next.customCss, { allowExternalFonts: next.allowExternalFonts, fontHosts: next.fontHosts });
    site.replace(appearanceView(next, verdict.css));
    return verdict;
  }

  /** Checks CSS without applying or storing it. */
  function check(css: string, policy: CssPolicy): SanitizedCss {
    return sanitizeCustomCss(css, policy);
  }

  /**
   * Applies and stores new CSS and font settings; returns the sanitizer's
   * verdict. With the server's document this replaces it for everyone and
   * rejects with the API error (409 conflict when another administrator
   * saved first, 400 custom_css_rejected); otherwise the browser keeps it.
   */
  async function save(css: string, policy: CssPolicy, document: Partial<AppearanceDocument> = {}): Promise<SanitizedCss> {
    const fontHosts = policy.fontHosts.slice(0, fontHostLimit);
    if (config.value !== null) {
      return stored(await saveSiteAppearance(client, { ...appearanceInput(config.value), ...document, customCss: css, allowExternalFonts: policy.allowExternalFonts, fontHosts }));
    }
    local.value = { css, policy: { allowExternalFonts: policy.allowExternalFonts, fontHosts } };
    writePersisted(storageKey, { css, allowExternalFonts: policy.allowExternalFonts, fontHosts });
    apply();
    return result.value;
  }

  /** Replaces the other members of the server document (theme, tokens, default layout). */
  async function saveDocument(document: Partial<AppearanceDocument>): Promise<void> {
    if (config.value === null) {
      throw new Error("site appearance is not available");
    }
    stored(await saveSiteAppearance(client, { ...appearanceInput(config.value), ...document }));
  }

  /** Clears the custom CSS (server document or browser copy). */
  async function clear(): Promise<void> {
    if (config.value !== null) {
      await save("", { allowExternalFonts: config.value.allowExternalFonts, fontHosts: config.value.fontHosts });
      return;
    }
    local.value = { css: "", policy: defaultCssPolicy };
    removePersisted(storageKey);
    apply();
  }

  /** Restores the whole default appearance on the server (G33.3). */
  async function reset(): Promise<void> {
    stored(await resetSiteAppearance(client));
  }

  /** Adopts documents this tab imported (G33.3). */
  function imported(next: SiteAppearanceConfig): void {
    stored(next);
  }

  watch(() => site.view, apply, { immediate: true });
  watch(
    () => [auth.user?.id, auth.user?.admin],
    () => {
      void loadConfig();
    },
    { immediate: true },
  );
  return { settings, config, source, result, supported, check, save, saveDocument, clear, reset, imported, loadConfig };
});
