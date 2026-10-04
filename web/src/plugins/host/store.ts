// Plugin host (G32.2–G32.4): validates manifests, keeps the administrator's
// enable/disable and order choices, loads enabled plugins lazily, runs their
// setup in isolation and exposes their contributions per hook. A plugin that
// fails to load, throws in setup or registers something invalid is marked
// failed and contributes nothing; render errors are caught by
// PluginBoundary.vue. Enable, disable and reorder take effect at once.
import {
  contributionIdPattern,
  isPluginRoutePath,
  pluginLocales,
  satisfies,
  SDK_VERSION,
  themeTokenNames,
  validateManifest,
  type HookMap,
  type HookName,
  type ManifestIssue,
  type ManifestWarning,
  type PluginContext,
  type PluginDefinition,
  type PluginLocale,
  type PluginManifest,
  type PluginMessages,
  type ThemeTokens,
} from "@jelee/plugin-sdk";
import { defineStore } from "pinia";
import { computed, inject, shallowRef, watch, watchEffect, type InjectionKey } from "vue";
import { routerKey } from "vue-router";
import { useApi } from "@/api";
import { currentLocaleKey } from "@/i18n";
import { readPersisted, writePersisted } from "@/stores/persist";
import { useToastStore } from "@/stores/toasts";
import { sanitizeTokenValue } from "@/theme/customCss";
import { applyStyleLayer } from "@/theme/styleSheets";
import { officialPlugins, type PluginBundle } from "./catalog";
import { createPluginApi } from "./restrictedApi";
import { clearPluginSettings, createPluginSettings } from "./settings";

export interface PluginHostOptions {
  readonly bundles?: readonly PluginBundle[];
  readonly jeleeVersion?: string;
}

/** Lets tests and embedders replace the bundled plugin list. */
export const pluginHostKey: InjectionKey<PluginHostOptions> = Symbol("jelee.pluginHost");

/** Version of this web client, compared with manifests' minJeleeVersion. */
export const jeleeVersion: string = __JELEE_VERSION__;

export type PluginStatus = "rejected" | "disabled" | "blocked" | "loading" | "active" | "failed";

/** Host-side reasons a plugin with a valid manifest does not run. */
export type PluginProblemCode =
  | "duplicate_id"
  | "dependency_missing"
  | "dependency_version"
  | "dependency_inactive"
  | "load_failed"
  | "invalid_definition"
  | "setup_failed";

export interface PluginProblem {
  readonly code: PluginProblemCode;
  readonly params: Readonly<Record<string, string>>;
}

export interface PluginView {
  readonly key: string;
  readonly manifest: PluginManifest | null;
  readonly official: boolean;
  readonly enabled: boolean;
  readonly status: PluginStatus;
  readonly issues: readonly ManifestIssue[];
  readonly warnings: readonly ManifestWarning[];
  readonly problems: readonly PluginProblem[];
  /** Render or action errors caught since the plugin was (re)started. */
  readonly failures: number;
  readonly lastFailure: string;
  /** Contributions per hook while active. */
  readonly contributionCounts: Readonly<Partial<Record<HookName, number>>>;
}

export interface Contribution<K extends HookName = HookName> {
  readonly pluginId: string;
  readonly hook: K;
  readonly value: HookMap[K];
  readonly context: PluginContext;
  /** Unique across plugins: "<plugin id>/<hook>/<contribution id>". */
  readonly key: string;
}

interface Runtime {
  readonly status: "loading" | "active" | "failed";
  readonly problem: PluginProblem | null;
  readonly contributions: readonly Contribution[];
}

interface Persisted {
  readonly enabled: Readonly<Record<string, boolean>>;
  readonly order: readonly string[];
}

const stateKey = "plugin-host.state";

function readState(): Persisted {
  const raw = readPersisted(stateKey);
  if (typeof raw !== "object" || raw === null) {
    return { enabled: {}, order: [] };
  }
  const enabledRaw = "enabled" in raw ? raw.enabled : null;
  const orderRaw = "order" in raw ? raw.order : null;
  const enabled: Record<string, boolean> = {};
  if (typeof enabledRaw === "object" && enabledRaw !== null) {
    for (const [id, value] of Object.entries(enabledRaw)) {
      if (typeof value === "boolean") {
        enabled[id] = value;
      }
    }
  }
  const order = Array.isArray(orderRaw) ? orderRaw.filter((id): id is string => typeof id === "string") : [];
  return { enabled, order };
}

function rawId(manifest: unknown): string | null {
  return typeof manifest === "object" && manifest !== null && "id" in manifest && typeof manifest.id === "string" ? manifest.id : null;
}

function isDefinition(value: unknown): value is PluginDefinition {
  if (typeof value !== "object" || value === null || !("setup" in value) || typeof value.setup !== "function" || !("messages" in value)) {
    return false;
  }
  const messages = value.messages;
  if (typeof messages !== "object" || messages === null) {
    return false;
  }
  const catalogs = pluginLocales.map((locale) => (locale in messages ? (messages as Record<string, unknown>)[locale] : undefined));
  if (catalogs.some((catalog) => typeof catalog !== "object" || catalog === null || Object.values(catalog).some((text) => typeof text !== "string"))) {
    return false;
  }
  // Every language carries the same keys, like the host catalogs.
  const keys = catalogs.map((catalog) => Object.keys(catalog as object).sort().join("\n"));
  return keys.every((list) => list === keys[0]);
}

function format(message: string, params: Readonly<Record<string, string | number>>): string {
  return message.replace(/\{\s*([A-Za-z0-9_]+)\s*\}/g, (whole, name: string) => (name in params ? String(params[name]) : whole));
}

function describe(error: unknown): string {
  const text = error instanceof Error ? error.name + ": " + error.message : String(error);
  return text.length > 200 ? text.slice(0, 197) + "..." : text;
}

/** Checks one contribution's shape; throws so the plugin's setup fails as a whole. */
function checkContribution(hook: HookName, value: unknown): string {
  if (typeof value !== "object" || value === null) {
    throw new TypeError(hook + ": contribution must be an object");
  }
  const record = value as Record<string, unknown>;
  const fn = (name: string) => {
    if (typeof record[name] !== "function") {
      throw new TypeError(hook + ": " + name + " must be a function");
    }
  };
  const text = (name: string) => {
    if (typeof record[name] !== "string" || record[name] === "") {
      throw new TypeError(hook + ": " + name + " must be a message key");
    }
  };
  if (hook === "route.register") {
    if (typeof record.path !== "string" || !isPluginRoutePath(record.path)) {
      throw new TypeError("route.register: invalid path");
    }
    text("title");
    fn("component");
    return record.path;
  }
  if (hook === "webhook.eventType") {
    if (typeof record.eventType !== "string" || !/^[a-z][a-z0-9_.]{0,63}$/.test(record.eventType)) {
      throw new TypeError("webhook.eventType: invalid event type");
    }
    text("label");
    return record.eventType;
  }
  if (typeof record.id !== "string" || !contributionIdPattern.test(record.id)) {
    throw new TypeError(hook + ": invalid id");
  }
  switch (hook) {
    case "media.detail.tabs":
    case "metadata.panel":
    case "settings.section":
      text("title");
      fn("component");
      break;
    case "library.toolbar":
      fn("component");
      break;
    case "item.action":
      text("label");
      fn("run");
      break;
    case "command.palette":
      text("label");
      fn("run");
      break;
    case "theme.token":
      fn("tokens");
      break;
  }
  return record.id;
}

export const usePluginStore = defineStore("plugins", () => {
  const options = inject(pluginHostKey, {});
  const bundles = options.bundles ?? officialPlugins();
  const version = options.jeleeVersion ?? jeleeVersion;
  const { client } = useApi();
  const currentLocale = inject(currentLocaleKey, () => "en-US");
  const router = inject(routerKey, null);
  const toasts = useToastStore();

  function locale(): PluginLocale {
    const current = currentLocale();
    return pluginLocales.find((candidate) => candidate === current) ?? "en-US";
  }

  // Manifests are validated once; a second plugin with an existing ID is refused.
  const seen = new Set<string>();
  const records = bundles.map((bundle, index) => {
    const result = validateManifest(bundle.manifest, { jeleeVersion: version, sdkVersion: SDK_VERSION });
    const key = result.ok ? result.manifest.id : (rawId(bundle.manifest) ?? `#${index}`);
    const duplicate = seen.has(key);
    seen.add(key);
    return { key: duplicate ? `${key}#${index}` : key, bundle, result, duplicate };
  });
  const byKey = new Map(records.map((record) => [record.key, record]));

  const persisted = shallowRef<Persisted>(readState());
  const runtime = shallowRef<Readonly<Record<string, Runtime>>>({});
  const failures = shallowRef<Readonly<Record<string, { count: number; last: string }>>>({});
  const generations = new Map<string, number>();
  const pending = new Set<Promise<void>>();

  function manifestOf(key: string): PluginManifest | null {
    const record = byKey.get(key);
    return record !== undefined && record.result.ok && !record.duplicate ? record.result.manifest : null;
  }

  function isEnabled(key: string): boolean {
    return persisted.value.enabled[key] ?? byKey.get(key)?.bundle.enabledByDefault ?? false;
  }

  const order = computed(() => {
    const known = records.map((record) => record.key);
    const listed = persisted.value.order.filter((key) => byKey.has(key));
    return [...listed, ...known.filter((key) => !listed.includes(key))];
  });

  /** Dependency problems of a valid, enabled plugin (transitively). */
  function dependencyProblems(key: string, visiting = new Set<string>()): PluginProblem[] {
    const manifest = manifestOf(key);
    if (manifest === null) {
      return [];
    }
    visiting.add(key);
    const problems: PluginProblem[] = [];
    for (const [dependency, range] of Object.entries(manifest.dependencies)) {
      const target = manifestOf(dependency);
      if (target === null) {
        problems.push({ code: "dependency_missing", params: { dependency, range } });
      } else if (!satisfies(target.version, range)) {
        problems.push({ code: "dependency_version", params: { dependency, range, version: target.version } });
      } else if (!isEnabled(dependency) || visiting.has(dependency) || dependencyProblems(dependency, visiting).length > 0) {
        problems.push({ code: "dependency_inactive", params: { dependency, range } });
      }
    }
    visiting.delete(key);
    return problems;
  }

  /** Plugins that should run, in administrator order. */
  const wanted = computed(() => order.value.filter((key) => manifestOf(key) !== null && isEnabled(key) && dependencyProblems(key).length === 0));

  function setRuntime(key: string, next: Runtime | null): void {
    const copy = Object.fromEntries(Object.entries(runtime.value).filter(([existing]) => existing !== key));
    runtime.value = next === null ? copy : { ...copy, [key]: next };
  }

  function reportFailure(key: string, error: unknown): void {
    const previous = failures.value[key]?.count ?? 0;
    failures.value = { ...failures.value, [key]: { count: previous + 1, last: describe(error) } };
  }

  function createContext(
    manifest: PluginManifest,
    definition: PluginDefinition,
    collect: (contribution: Contribution) => void,
  ): { context: PluginContext; close: () => void } {
    const messages: Readonly<Record<PluginLocale, PluginMessages>> = definition.messages;
    const t = (key: string, params: Readonly<Record<string, string | number>> = {}) => {
      const message = messages[locale()][key] ?? messages["en-US"][key];
      return message === undefined ? key : format(message, params);
    };
    const ui = Object.freeze({
      notify(key: string, tone: "info" | "success" | "danger" = "info", params: Readonly<Record<string, string | number>> = {}) {
        toasts.push("plugins.notice", tone, { message: t(key, params) });
      },
    });
    const declared = new Set<string>(manifest.hooks);
    const used = new Set<string>();
    let open = true;
    const context: PluginContext = Object.freeze({
      id: manifest.id,
      manifest,
      sdkVersion: SDK_VERSION,
      jeleeVersion: version,
      api: createPluginApi(client, manifest.permissions),
      settings: createPluginSettings(manifest.id, manifest.permissions.includes("settings.storage")),
      ui,
      locale,
      t,
      register<K extends HookName>(hook: K, value: HookMap[K]) {
        if (!open) {
          throw new Error("register() is only available during setup");
        }
        if (!declared.has(hook)) {
          throw new Error("hook not declared in manifest: " + hook);
        }
        const id = checkContribution(hook, value);
        const key = manifest.id + "/" + hook + "/" + id;
        if (used.has(key)) {
          throw new Error("duplicate contribution: " + key);
        }
        used.add(key);
        collect({ pluginId: manifest.id, hook, value: Object.freeze({ ...(value as object) }) as HookMap[K], context, key });
      },
    });
    return {
      context,
      close: () => {
        open = false;
      },
    };
  }

  function activate(key: string): void {
    const manifest = manifestOf(key);
    const record = byKey.get(key);
    if (manifest === null || record === undefined) {
      return;
    }
    const generation = (generations.get(key) ?? 0) + 1;
    generations.set(key, generation);
    const current = () => generations.get(key) === generation;
    setRuntime(key, { status: "loading", problem: null, contributions: [] });
    const task = (async () => {
      let loaded: unknown;
      try {
        loaded = await record.bundle.load(manifest.entry);
      } catch (error: unknown) {
        if (current()) {
          setRuntime(key, { status: "failed", problem: { code: "load_failed", params: { error: describe(error) } }, contributions: [] });
        }
        return;
      }
      if (!current()) {
        return;
      }
      const definition: unknown = typeof loaded === "object" && loaded !== null && "default" in loaded ? loaded.default : loaded;
      if (!isDefinition(definition)) {
        setRuntime(key, { status: "failed", problem: { code: "invalid_definition", params: {} }, contributions: [] });
        return;
      }
      const collected: Contribution[] = [];
      try {
        const { context, close } = createContext(manifest, definition, (contribution) => collected.push(contribution));
        try {
          definition.setup(context);
        } finally {
          close();
        }
      } catch (error: unknown) {
        setRuntime(key, { status: "failed", problem: { code: "setup_failed", params: { error: describe(error) } }, contributions: [] });
        return;
      }
      setRuntime(key, { status: "active", problem: null, contributions: collected });
    })();
    pending.add(task);
    void task.finally(() => pending.delete(task));
  }

  function deactivate(key: string): void {
    generations.set(key, (generations.get(key) ?? 0) + 1);
    setRuntime(key, null);
  }

  watch(
    wanted,
    (keys) => {
      for (const key of keys) {
        if (runtime.value[key] === undefined) {
          activate(key);
        }
      }
      for (const key of Object.keys(runtime.value)) {
        if (!keys.includes(key)) {
          deactivate(key);
        }
      }
    },
    { immediate: true },
  );

  /** Resolves once every started plugin finished loading. */
  async function settled(): Promise<void> {
    while (pending.size > 0) {
      await Promise.allSettled([...pending]);
    }
  }

  /** Active contributions of one hook, in plugin order. */
  function contributions<K extends HookName>(hook: K): Contribution<K>[] {
    const result: Contribution<K>[] = [];
    for (const key of wanted.value) {
      const state = runtime.value[key];
      if (state?.status === "active") {
        for (const contribution of state.contributions) {
          if (contribution.hook === hook) {
            result.push(contribution as Contribution<K>);
          }
        }
      }
    }
    return result;
  }

  const plugins = computed<PluginView[]>(() =>
    order.value.map((key) => {
      const record = byKey.get(key);
      const manifest = manifestOf(key);
      const enabled = isEnabled(key);
      const state = runtime.value[key];
      const problems: PluginProblem[] = record?.duplicate === true ? [{ code: "duplicate_id", params: { id: key.replace(/#\d+$/, "") } }] : [];
      let status: PluginStatus;
      if (manifest === null) {
        status = "rejected";
      } else if (!enabled) {
        status = "disabled";
      } else {
        const blocked = dependencyProblems(key);
        problems.push(...blocked);
        status = blocked.length > 0 ? "blocked" : (state?.status ?? "loading");
        if (state?.problem) {
          problems.push(state.problem);
        }
      }
      const counts: Partial<Record<HookName, number>> = {};
      for (const contribution of state?.status === "active" ? state.contributions : []) {
        counts[contribution.hook] = (counts[contribution.hook] ?? 0) + 1;
      }
      return {
        key,
        manifest,
        official: record?.bundle.official ?? false,
        enabled,
        status,
        issues: record !== undefined && !record.result.ok ? record.result.issues : [],
        warnings: record !== undefined && record.result.ok ? record.result.warnings : [],
        problems,
        failures: failures.value[key]?.count ?? 0,
        lastFailure: failures.value[key]?.last ?? "",
        contributionCounts: counts,
      };
    }),
  );

  function save(next: Persisted): void {
    persisted.value = next;
    writePersisted(stateKey, next);
  }

  function setEnabled(key: string, enabled: boolean): void {
    if (byKey.has(key)) {
      save({ ...persisted.value, enabled: { ...persisted.value.enabled, [key]: enabled } });
      if (enabled) {
        failures.value = { ...failures.value, [key]: { count: 0, last: "" } };
      }
    }
  }

  function setOrder(keys: readonly string[]): void {
    const valid = keys.filter((key) => byKey.has(key));
    save({ ...persisted.value, order: [...valid, ...order.value.filter((key) => !valid.includes(key))] });
  }

  /** Restarts a plugin: clears caught failures and runs its setup again. */
  function restart(key: string): void {
    failures.value = { ...failures.value, [key]: { count: 0, last: "" } };
    if (runtime.value[key] !== undefined) {
      deactivate(key);
      activate(key);
    }
  }

  /** Deletes a plugin's settings namespace and restarts it. */
  function clearSettings(key: string): void {
    const manifest = manifestOf(key);
    if (manifest !== null) {
      clearPluginSettings(manifest.id);
      restart(key);
    }
  }

  // theme.token: one constructed stylesheet for all active overrides; values
  // pass the same checks as administrator CSS.
  watchEffect(() => {
    const light: string[] = [];
    const dark: string[] = [];
    for (const contribution of contributions("theme.token")) {
      try {
        const add = (tokens: ThemeTokens, into: string[]) => {
          for (const [name, value] of Object.entries(tokens)) {
            const safe = typeof value === "string" ? sanitizeTokenValue(value, name === "font-family") : null;
            if (safe !== null && themeTokenSet.has(name)) {
              into.push("--jl-" + name + ":" + safe);
            }
          }
        };
        const tokens = contribution.value.tokens();
        add(tokens, light);
        add(contribution.value.darkTokens?.() ?? tokens, dark);
      } catch (error: unknown) {
        reportFailure(contribution.pluginId, error);
      }
    }
    const css =
      light.length === 0 && dark.length === 0
        ? ""
        : `:root{${light.join(";")}}\n@media (prefers-color-scheme: dark){:root:not([data-theme="light"]){${dark.join(";")}}}\n:root[data-theme="dark"]{${dark.join(";")}}`;
    applyStyleLayer("plugin-tokens", css);
  });

  // route.register: pages at /x/<plugin id>/<path>, added and removed with the plugin.
  const routeNames = new Set<string>();
  if (router !== null) {
    watch(
      () => contributions("route.register"),
      (routes) => {
        const next = new Map(routes.map((route) => ["plugin:" + route.key, route]));
        for (const name of routeNames) {
          if (!next.has(name)) {
            router.removeRoute(name);
            routeNames.delete(name);
          }
        }
        for (const [name, route] of next) {
          if (!routeNames.has(name)) {
            router.addRoute({
              path: "/x/" + route.pluginId + "/" + route.value.path,
              name,
              component: () => import("./PluginRouteView.vue"),
              props: { contributionKey: route.key },
            });
            routeNames.add(name);
          }
        }
      },
      { immediate: true },
    );
  }

  return { plugins, order, contributions, settled, setEnabled, setOrder, restart, clearSettings, reportFailure, locale };
});

const themeTokenSet: ReadonlySet<string> = new Set<string>(themeTokenNames);
