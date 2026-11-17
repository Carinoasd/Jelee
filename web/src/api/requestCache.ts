// Request de-duplication and a short-lived cache for read-only catalog pages
// (G34.2, G35.3). Identical requests in flight share one promise, and a
// resolved page is reused for a few seconds, so typing back and forth in the
// search box, returning to a library or the four kind groups of a search do
// not repeat requests. Failures are never cached. Entries belong to one API
// client (one per app) and are dropped when the signed-in user changes, so
// one account never sees another account's pages; a retry or reload passes
// force to skip the cached copy.

/** How long a resolved page is reused. */
export const requestCacheTtlMs = 15_000;
/** Entries kept per client; the oldest go first. */
export const requestCacheLimit = 200;

interface Entry {
  readonly promise: Promise<unknown>;
  /** Milliseconds since the epoch; Infinity while in flight. */
  expires: number;
}

const caches = new WeakMap<object, Map<string, Entry>>();

function cacheOf(owner: object): Map<string, Entry> {
  let cache = caches.get(owner);
  if (cache === undefined) {
    cache = new Map();
    caches.set(owner, cache);
  }
  return cache;
}

export interface CacheOptions {
  /** Skips a resolved copy (an in-flight request is still shared). */
  readonly force?: boolean;
  readonly now?: () => number;
}

/** Runs load once per key: shares requests in flight and reuses fresh results. */
export function cachedRequest<T>(owner: object, key: string, load: () => Promise<T>, options: CacheOptions = {}): Promise<T> {
  const now = options.now ?? Date.now;
  const cache = cacheOf(owner);
  const hit = cache.get(key);
  if (hit !== undefined && (hit.expires === Infinity || (options.force !== true && hit.expires > now()))) {
    return hit.promise as Promise<T>;
  }
  const promise = load();
  const entry: Entry = { promise, expires: Infinity };
  cache.delete(key);
  cache.set(key, entry);
  while (cache.size > requestCacheLimit) {
    const oldest = cache.keys().next().value;
    if (oldest === undefined) {
      break;
    }
    cache.delete(oldest);
  }
  promise.then(
    () => {
      entry.expires = now() + requestCacheTtlMs;
    },
    () => {
      if (cache.get(key) === entry) {
        cache.delete(key);
      }
    },
  );
  return promise;
}

/** Drops every entry of a client (sign-out, another account). */
export function clearRequestCache(owner: object): void {
  caches.get(owner)?.clear();
}
