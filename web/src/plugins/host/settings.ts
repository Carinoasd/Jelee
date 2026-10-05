// Per-plugin settings namespace (G32.4), reachable only through the owning
// plugin's context. Two layers, both reactive and size-limited:
// - site values: the administrator's settings for every user, stored on the
//   server (settings.<plugin ID> of /api/v1/site/plugins). An administrator's
//   set() and remove() write here when the server is available.
// - browser values: "plugin-settings.<id>" in this browser. Other users'
//   changes, and everyone's while signed out or when the server cannot be
//   read, stay here and take precedence over the site values in this
//   browser only.
// A plugin without settings.storage reads fallbacks and cannot write.
import { PluginPermissionError, type PluginSettingValue, type PluginSettings } from "@jelee/plugin-sdk";
import { shallowRef } from "vue";
import { readPersisted, removePersisted, writePersisted } from "@/stores/persist";

export const pluginSettingsMaxBytes = 16 * 1024;
const maxKeys = 64;
const settingKey = /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/;

export type PluginSettingValues = Readonly<Record<string, PluginSettingValue>>;

/** Where site-wide values come from and, for administrators, go to. */
export interface PluginSettingsBackend {
  /** The plugin's site-wide values (reactive); empty when there are none. */
  site(): Readonly<Record<string, unknown>>;
  /** Whether set() and remove() write the site-wide values. */
  writesSite(): boolean;
  /** Replaces the plugin's site-wide values; applies them at once. */
  saveSite(values: PluginSettingValues): void;
}

const browserOnly: PluginSettingsBackend = { site: () => ({}), writesSite: () => false, saveSite: () => undefined };

const storageKey = "plugin-settings";

function isSettingValue(value: unknown, depth = 0): value is PluginSettingValue {
  if (depth > 8) {
    return false;
  }
  if (value === null || typeof value === "string" || typeof value === "boolean") {
    return true;
  }
  if (typeof value === "number") {
    return Number.isFinite(value);
  }
  if (Array.isArray(value)) {
    return value.every((entry) => isSettingValue(entry, depth + 1));
  }
  if (typeof value === "object") {
    return Object.entries(value).every(([key, entry]) => settingKey.test(key) && isSettingValue(entry, depth + 1));
  }
  return false;
}

/** Keeps the valid entries of an untrusted namespace (storage or server). */
export function validSettings(stored: unknown): PluginSettingValues {
  if (typeof stored !== "object" || stored === null || Array.isArray(stored)) {
    return {};
  }
  const values: Record<string, PluginSettingValue> = {};
  for (const [key, value] of Object.entries(stored).slice(0, maxKeys)) {
    if (settingKey.test(key) && isSettingValue(value)) {
      values[key] = value;
    }
  }
  return values;
}

function checkSize(next: PluginSettingValues): void {
  const size = new TextEncoder().encode(JSON.stringify(next)).length;
  if (size > pluginSettingsMaxBytes || Object.keys(next).length > maxKeys) {
    throw new RangeError(`plugin settings exceed ${pluginSettingsMaxBytes} bytes or ${maxKeys} keys`);
  }
}

export function createPluginSettings(pluginId: string, granted: boolean, backend: PluginSettingsBackend = browserOnly): PluginSettings {
  const local = shallowRef<PluginSettingValues>(granted ? validSettings(readPersisted(storageKey, pluginId)) : {});
  const site = (): PluginSettingValues => (granted ? validSettings(backend.site()) : {});
  const need = () => {
    if (!granted) {
      throw new PluginPermissionError("settings.storage");
    }
  };
  const check = (key: string) => {
    if (!settingKey.test(key)) {
      throw new TypeError("invalid plugin setting key: " + key);
    }
  };
  const saveLocal = (next: PluginSettingValues) => {
    checkSize(next);
    local.value = next;
    if (Object.keys(next).length === 0) {
      removePersisted(storageKey, pluginId);
    } else {
      writePersisted(storageKey, next, pluginId);
    }
  };
  const without = (values: PluginSettingValues, key: string) => Object.fromEntries(Object.entries(values).filter(([existing]) => existing !== key));
  const change = (key: string, value: PluginSettingValue | undefined) => {
    if (backend.writesSite()) {
      const next = value === undefined ? without(site(), key) : { ...site(), [key]: value };
      checkSize(next);
      backend.saveSite(next);
      // A browser value would hide the site-wide one from this administrator.
      if (key in local.value) {
        saveLocal(without(local.value, key));
      }
      return;
    }
    saveLocal(value === undefined ? without(local.value, key) : { ...local.value, [key]: value });
  };
  return Object.freeze({
    get<T extends PluginSettingValue>(key: string, fallback: T): T {
      check(key);
      const value = key in local.value ? local.value[key] : site()[key];
      // A stored value of another type (edited storage, older plugin
      // version) falls back instead of reaching the plugin.
      if (value === undefined || typeof value !== typeof fallback || Array.isArray(value) !== Array.isArray(fallback)) {
        return fallback;
      }
      return value as T;
    },
    set(key: string, value: PluginSettingValue) {
      need();
      check(key);
      if (!isSettingValue(value)) {
        throw new TypeError("plugin setting values must be JSON");
      }
      change(key, value);
    },
    remove(key: string) {
      need();
      check(key);
      change(key, undefined);
    },
    keys() {
      return [...new Set([...Object.keys(site()), ...Object.keys(local.value)])];
    },
  });
}

/** Removes every setting of a plugin stored in this browser (administration page). */
export function clearPluginSettings(pluginId: string): void {
  removePersisted(storageKey, pluginId);
}
