// The one place the web client touches browser storage (eslint.config.js
// allows localStorage in this file only). It keeps presentation state that
// has no server API yet: layouts (G33.5), plugin enablement and plugin
// settings (G32.4) and administrator CSS (G33.4). Credentials, CSRF values
// and session data never come here (G35.1): keys naming them are refused,
// every value is JSON with a size limit, and everything read back is
// treated as untrusted input by its caller (CSS is sanitized again before it
// is applied, layouts and plugin states are normalized).

const prefix = "jelee.ui.v1.";
/** Largest serialized value accepted, in UTF-16 code units. */
export const persistMaxLength = 128 * 1024;
const keyPattern = /^[a-z][a-z0-9-]*(?:[.:][a-z0-9-]+)*$/i;
const credentialWords = /token|csrf|session|cookie|password|secret|credential|bearer|auth/i;

function storage(): Storage | null {
  try {
    return globalThis.localStorage;
  } catch {
    // Disabled storage (privacy mode, sandboxed frame).
    return null;
  }
}

const scopePattern = /^[a-z0-9][a-z0-9.-]{0,127}$/i;

/**
 * Builds the storage name. The key is a fixed name chosen by the web client
 * and must not name credential material; the optional scope is data that
 * picks one instance (a plugin ID, an account ID) and is only syntax-checked.
 */
function checkKey(key: string, scope?: string): string {
  if (!keyPattern.test(key) || key.length > 64 || credentialWords.test(key)) {
    throw new TypeError("invalid persisted key: " + key);
  }
  if (scope !== undefined && !scopePattern.test(scope)) {
    throw new TypeError("invalid persisted scope: " + scope);
  }
  return prefix + key + (scope === undefined ? "" : "." + scope);
}

/** Reads and parses one value; null when absent, unreadable or not JSON. */
export function readPersisted(key: string, scope?: string): unknown {
  const name = checkKey(key, scope);
  try {
    const raw = storage()?.getItem(name) ?? null;
    if (raw === null || raw.length > persistMaxLength) {
      return null;
    }
    return JSON.parse(raw) as unknown;
  } catch {
    return null;
  }
}

/** Stores one JSON value; returns false when it is too large or storage fails. */
export function writePersisted(key: string, value: unknown, scope?: string): boolean {
  const name = checkKey(key, scope);
  const text = JSON.stringify(value);
  if (text.length > persistMaxLength) {
    return false;
  }
  try {
    const target = storage();
    if (target === null) {
      return false;
    }
    target.setItem(name, text);
    return true;
  } catch {
    // Quota exceeded or storage disabled: the change stays in memory.
    return false;
  }
}

export function removePersisted(key: string, scope?: string): void {
  const name = checkKey(key, scope);
  try {
    storage()?.removeItem(name);
  } catch {
    // Nothing to remove.
  }
}
