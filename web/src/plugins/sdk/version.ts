// Version and deprecation policy of @jelee/plugin-sdk (G32.6). The SDK
// follows semantic versioning: a MAJOR release may remove or change hooks and
// context APIs and ships a migration section in docs/plugin-development.md; a
// MINOR release only adds; a PATCH release only fixes. The host supports
// exactly one SDK major at a time, so a plugin whose sdkVersion range does not
// include SDK_VERSION is refused before its code is loaded.

/** Version of the SDK this host implements. */
export const SDK_VERSION = "1.0.0";

/** A hook or context API that still works but will be removed. */
export interface Deprecation {
  /** SDK version that deprecated it. */
  readonly since: string;
  /** First SDK version without it (always a new major). */
  readonly removedIn: string;
  /** Hook or API to use instead, if any. */
  readonly replacement?: string;
}

/**
 * Deprecated hooks of the current major. A manifest that declares one still
 * loads; the administration page shows a warning with the replacement.
 * Deprecated entries stay for at least one minor release and six months and
 * are removed only by the next major. SDK 1.0.0 has none yet.
 */
export const deprecatedHooks: Readonly<Record<string, Deprecation>> = Object.freeze({});
