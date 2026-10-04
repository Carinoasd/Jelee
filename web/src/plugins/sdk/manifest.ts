// Manifest validation (G32.2). The host validates every manifest before it
// imports the plugin's entry: a malformed manifest, an SDK range that
// excludes this host, a Jelee version that is too old, an unknown
// permission or hook, or a hook without its permission refuses the plugin
// with issues the administration page shows in the interface language.
import { deprecatedHooks, SDK_VERSION, type Deprecation } from "./version";
import { hookPermissions, isHookName, type HookName } from "./hooks";
import { compareVersions, parseRange, parseVersion, satisfies } from "./semver";
import { pluginLocales, pluginPermissions, type LocalizedText, type PluginManifest, type PluginPermission } from "./types";

/** Why a manifest was refused; params feed the localized message. */
export type ManifestIssueCode =
  | "not_object"
  | "unknown_field"
  | "invalid_id"
  | "invalid_text"
  | "invalid_version"
  | "invalid_sdk_range"
  | "sdk_incompatible"
  | "invalid_min_jelee"
  | "jelee_too_old"
  | "invalid_permissions"
  | "unknown_permission"
  | "invalid_hooks"
  | "unknown_hook"
  | "hook_needs_permission"
  | "invalid_dependencies"
  | "invalid_entry"
  | "invalid_author";

export interface ManifestIssue {
  readonly code: ManifestIssueCode;
  readonly field: string;
  readonly params: Readonly<Record<string, string>>;
}

export interface ManifestWarning {
  readonly code: "hook_deprecated";
  readonly hook: string;
  readonly deprecation: Deprecation;
}

export type ManifestResult =
  | { readonly ok: true; readonly manifest: PluginManifest; readonly warnings: readonly ManifestWarning[] }
  | { readonly ok: false; readonly issues: readonly ManifestIssue[] };

export interface ManifestEnvironment {
  readonly sdkVersion?: string;
  /** Version of the running Jelee web client. */
  readonly jeleeVersion: string;
  readonly deprecations?: Readonly<Record<string, Deprecation>>;
}

export const pluginIdPattern = /^[a-z][a-z0-9-]{0,31}(?:\.[a-z][a-z0-9-]{0,31}){1,3}$/;
const entryPattern = /^\.\/[a-z][a-z0-9-]{0,47}\.(?:ts|js)$/;
const knownFields = new Set([
  "id",
  "name",
  "description",
  "version",
  "sdkVersion",
  "minJeleeVersion",
  "permissions",
  "hooks",
  "dependencies",
  "entry",
  "author",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function localizedText(value: unknown, max: number): LocalizedText | null {
  if (!isRecord(value) || Object.keys(value).length !== pluginLocales.length) {
    return null;
  }
  const result: Partial<Record<(typeof pluginLocales)[number], string>> = {};
  for (const locale of pluginLocales) {
    const text = value[locale];
    if (typeof text !== "string" || text.trim() === "" || text.length > max) {
      return null;
    }
    result[locale] = text;
  }
  return result as LocalizedText;
}

function issue(code: ManifestIssueCode, field: string, params: Record<string, string> = {}): ManifestIssue {
  return { code, field, params };
}

/** Validates an untrusted manifest object against this host. */
export function validateManifest(raw: unknown, environment: ManifestEnvironment): ManifestResult {
  const sdkVersion = environment.sdkVersion ?? SDK_VERSION;
  const deprecations = environment.deprecations ?? deprecatedHooks;
  if (!isRecord(raw)) {
    return { ok: false, issues: [issue("not_object", "")] };
  }
  const issues: ManifestIssue[] = [];
  for (const field of Object.keys(raw)) {
    if (!knownFields.has(field)) {
      issues.push(issue("unknown_field", field, { field }));
    }
  }

  const id = raw.id;
  if (typeof id !== "string" || !pluginIdPattern.test(id)) {
    issues.push(issue("invalid_id", "id"));
  }
  const name = localizedText(raw.name, 80);
  if (name === null) {
    issues.push(issue("invalid_text", "name", { field: "name" }));
  }
  const description = localizedText(raw.description, 400);
  if (description === null) {
    issues.push(issue("invalid_text", "description", { field: "description" }));
  }
  const version = typeof raw.version === "string" ? parseVersion(raw.version) : null;
  if (version === null) {
    issues.push(issue("invalid_version", "version"));
  }

  const sdkRange = typeof raw.sdkVersion === "string" ? parseRange(raw.sdkVersion) : null;
  if (sdkRange === null) {
    issues.push(issue("invalid_sdk_range", "sdkVersion"));
  } else if (!satisfies(sdkVersion, sdkRange)) {
    issues.push(issue("sdk_incompatible", "sdkVersion", { required: String(raw.sdkVersion), current: sdkVersion }));
  }

  const minJelee = typeof raw.minJeleeVersion === "string" ? parseVersion(raw.minJeleeVersion) : null;
  const running = parseVersion(environment.jeleeVersion);
  if (minJelee === null) {
    issues.push(issue("invalid_min_jelee", "minJeleeVersion"));
  } else if (running === null || compareVersions(running, minJelee) < 0) {
    issues.push(issue("jelee_too_old", "minJeleeVersion", { required: String(raw.minJeleeVersion), current: environment.jeleeVersion }));
  }

  const permissions: PluginPermission[] = [];
  if (!Array.isArray(raw.permissions) || new Set(raw.permissions).size !== raw.permissions.length) {
    issues.push(issue("invalid_permissions", "permissions"));
  } else {
    for (const permission of raw.permissions as unknown[]) {
      if (typeof permission === "string" && (pluginPermissions as readonly string[]).includes(permission)) {
        permissions.push(permission as PluginPermission);
      } else {
        issues.push(issue("unknown_permission", "permissions", { permission: String(permission) }));
      }
    }
  }

  const hooks: HookName[] = [];
  const warnings: ManifestWarning[] = [];
  if (!Array.isArray(raw.hooks) || raw.hooks.length === 0 || new Set(raw.hooks).size !== raw.hooks.length) {
    issues.push(issue("invalid_hooks", "hooks"));
  } else {
    for (const hook of raw.hooks as unknown[]) {
      if (!isHookName(hook)) {
        issues.push(issue("unknown_hook", "hooks", { hook: String(hook) }));
        continue;
      }
      hooks.push(hook);
      const needed = hookPermissions[hook];
      if (needed !== undefined && !permissions.includes(needed)) {
        issues.push(issue("hook_needs_permission", "hooks", { hook, permission: needed }));
      }
      const deprecation = deprecations[hook];
      if (deprecation !== undefined) {
        warnings.push({ code: "hook_deprecated", hook, deprecation });
      }
    }
  }

  const dependencies: Record<string, string> = {};
  const rawDependencies = raw.dependencies ?? {};
  if (!isRecord(rawDependencies)) {
    issues.push(issue("invalid_dependencies", "dependencies"));
  } else {
    for (const [dependency, range] of Object.entries(rawDependencies)) {
      if (!pluginIdPattern.test(dependency) || dependency === id || typeof range !== "string" || parseRange(range) === null) {
        issues.push(issue("invalid_dependencies", "dependencies", { dependency }));
      } else {
        dependencies[dependency] = range;
      }
    }
  }

  if (typeof raw.entry !== "string" || !entryPattern.test(raw.entry)) {
    issues.push(issue("invalid_entry", "entry"));
  }
  if (raw.author !== undefined && (typeof raw.author !== "string" || raw.author.trim() === "" || raw.author.length > 80)) {
    issues.push(issue("invalid_author", "author"));
  }

  if (issues.length > 0 || name === null || description === null) {
    return { ok: false, issues };
  }
  const manifest: PluginManifest = {
    id: id as string,
    name,
    description,
    version: raw.version as string,
    sdkVersion: raw.sdkVersion as string,
    minJeleeVersion: raw.minJeleeVersion as string,
    permissions,
    hooks,
    dependencies,
    entry: raw.entry as string,
    ...(typeof raw.author === "string" ? { author: raw.author } : {}),
  };
  return { ok: true, manifest: deepFreeze(manifest), warnings };
}

function deepFreeze<T>(value: T): T {
  if (typeof value === "object" && value !== null) {
    for (const child of Object.values(value)) {
      deepFreeze(child);
    }
    Object.freeze(value);
  }
  return value;
}
