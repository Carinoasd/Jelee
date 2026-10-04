// Per-plugin settings namespace (G32.4): stored under
// "plugin-settings.<id>" in this browser (no server API yet, see
// docs/frontend-adr.md), reactive, size-limited, and reachable only through
// the owning plugin's context. A plugin without settings.storage reads
// fallbacks and cannot write.
import { PluginPermissionError, type PluginSettingValue, type PluginSettings } from "@jelee/plugin-sdk";
import { shallowRef } from "vue";
import { readPersisted, removePersisted, writePersisted } from "@/stores/persist";

export const pluginSettingsMaxBytes = 16 * 1024;
const maxKeys = 64;
const settingKey = /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/;

type Values = Readonly<Record<string, PluginSettingValue>>;

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

function load(pluginId: string): Values {
  const stored = readPersisted(storageKey, pluginId);
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

export function createPluginSettings(pluginId: string, granted: boolean): PluginSettings {
  const values = shallowRef<Values>(granted ? load(pluginId) : {});
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
  const save = (next: Values) => {
    const size = new TextEncoder().encode(JSON.stringify(next)).length;
    if (size > pluginSettingsMaxBytes || Object.keys(next).length > maxKeys) {
      throw new RangeError(`plugin settings exceed ${pluginSettingsMaxBytes} bytes or ${maxKeys} keys`);
    }
    values.value = next;
    writePersisted(storageKey, next, pluginId);
  };
  return Object.freeze({
    get<T extends PluginSettingValue>(key: string, fallback: T): T {
      check(key);
      const value = values.value[key];
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
      save({ ...values.value, [key]: value });
    },
    remove(key: string) {
      need();
      check(key);
      save(Object.fromEntries(Object.entries(values.value).filter(([existing]) => existing !== key)));
    },
    keys() {
      return Object.keys(values.value);
    },
  });
}

/** Removes every stored setting of a plugin (administration page). */
export function clearPluginSettings(pluginId: string): void {
  removePersisted(storageKey, pluginId);
}
