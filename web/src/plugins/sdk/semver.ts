// Minimal semantic versioning (https://semver.org) for plugin manifests:
// versions MAJOR.MINOR.PATCH with an optional prerelease, and npm-style
// ranges ("^1.2.0", "~1.2.0", "1.x", ">=1.0.0 <2.0.0", "1.0.0 || 2.0.0",
// "*"). Build metadata is accepted and ignored. Kept in-tree so the SDK has
// no runtime dependency besides Vue.

export interface SemVer {
  readonly major: number;
  readonly minor: number;
  readonly patch: number;
  readonly prerelease: readonly string[];
}

const versionPattern =
  /^(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

/** Parses a full version; returns null for anything that is not strict SemVer. */
export function parseVersion(text: string): SemVer | null {
  const match = versionPattern.exec(text.trim());
  if (match === null) {
    return null;
  }
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    prerelease: match[4] === undefined ? [] : match[4].split("."),
  };
}

function compareIdentifiers(a: string, b: string): number {
  const numericA = /^\d+$/.test(a);
  const numericB = /^\d+$/.test(b);
  if (numericA && numericB) {
    return Math.sign(Number(a) - Number(b));
  }
  if (numericA !== numericB) {
    return numericA ? -1 : 1;
  }
  return a < b ? -1 : a > b ? 1 : 0;
}

/** Orders two versions: negative, zero or positive like Array.sort. */
export function compareVersions(a: SemVer, b: SemVer): number {
  for (const key of ["major", "minor", "patch"] as const) {
    if (a[key] !== b[key]) {
      return Math.sign(a[key] - b[key]);
    }
  }
  // A prerelease sorts before its release.
  if (a.prerelease.length === 0 || b.prerelease.length === 0) {
    return b.prerelease.length - a.prerelease.length === 0 ? 0 : a.prerelease.length === 0 ? 1 : -1;
  }
  const length = Math.max(a.prerelease.length, b.prerelease.length);
  for (let i = 0; i < length; i++) {
    const left = a.prerelease[i];
    const right = b.prerelease[i];
    if (left === undefined || right === undefined) {
      return left === undefined ? -1 : 1;
    }
    const order = compareIdentifiers(left, right);
    if (order !== 0) {
      return order;
    }
  }
  return 0;
}

type Operator = "<" | "<=" | ">" | ">=" | "=";
interface Comparator {
  readonly operator: Operator;
  readonly version: SemVer;
}

const partialPattern = /^(?:[v=])?(\d+|[xX*])(?:\.(\d+|[xX*]))?(?:\.(\d+|[xX*]))?(?:-([0-9A-Za-z.-]+))?$/;

interface Partial {
  readonly parts: readonly (number | null)[];
  readonly prerelease: readonly string[];
}

function parsePartial(text: string): Partial | null {
  const match = partialPattern.exec(text);
  if (match === null) {
    return null;
  }
  const parts = [match[1], match[2], match[3]].map((part) => (part === undefined || /^[xX*]$/.test(part) ? null : Number(part)));
  // "1.x.3" is not a range.
  const firstWildcard = parts.indexOf(null);
  if (firstWildcard >= 0 && parts.slice(firstWildcard).some((part) => part !== null)) {
    return null;
  }
  if (parts.some((part) => part !== null && part > 999_999_999)) {
    return null;
  }
  return { parts, prerelease: match[4] === undefined ? [] : match[4].split(".") };
}

function version(major: number, minor: number, patch: number, prerelease: readonly string[] = []): SemVer {
  return { major, minor, patch, prerelease };
}

/** Expands one range token (with or without operator) into comparators. */
function expand(token: string): Comparator[] | null {
  const operatorMatch = /^(\^|~|>=|<=|>|<|=)?(.*)$/.exec(token);
  const operator = operatorMatch?.[1] ?? "";
  const partial = parsePartial(operatorMatch?.[2] ?? "");
  if (partial === null) {
    return null;
  }
  const [major, minor, patch] = partial.parts;
  if (major === null || major === undefined) {
    // "*" or "x": any version, but only with a plain or equality operator.
    return operator === "" || operator === "=" || operator === ">=" ? [] : null;
  }
  const low = version(major, minor ?? 0, patch ?? 0, partial.prerelease);
  const full = minor !== null && minor !== undefined && patch !== null && patch !== undefined;
  switch (operator) {
    case "^": {
      const upper =
        major > 0 || minor === null || minor === undefined
          ? version(major + 1, 0, 0, ["0"])
          : minor > 0 || patch === null || patch === undefined
            ? version(0, minor + 1, 0, ["0"])
            : version(0, 0, patch + 1, ["0"]);
      return [
        { operator: ">=", version: low },
        { operator: "<", version: upper },
      ];
    }
    case "~": {
      const upper = minor === null || minor === undefined ? version(major + 1, 0, 0, ["0"]) : version(major, minor + 1, 0, ["0"]);
      return [
        { operator: ">=", version: low },
        { operator: "<", version: upper },
      ];
    }
    case "":
    case "=": {
      if (full) {
        return [{ operator: "=", version: low }];
      }
      const upper = minor === null || minor === undefined ? version(major + 1, 0, 0, ["0"]) : version(major, minor + 1, 0, ["0"]);
      return [
        { operator: ">=", version: low },
        { operator: "<", version: upper },
      ];
    }
    default:
      if (!full && (operator === ">" || operator === "<=")) {
        // ">1.2" means ">=1.3.0"; "<=1.2" means "<1.3.0".
        const next = minor === null || minor === undefined ? version(major + 1, 0, 0) : version(major, minor + 1, 0);
        return [{ operator: operator === ">" ? ">=" : "<", version: next }];
      }
      return [{ operator: operator as Operator, version: low }];
  }
}

function test(comparator: Comparator, candidate: SemVer): boolean {
  const order = compareVersions(candidate, comparator.version);
  switch (comparator.operator) {
    case "<":
      return order < 0;
    case "<=":
      return order <= 0;
    case ">":
      return order > 0;
    case ">=":
      return order >= 0;
    case "=":
      return order === 0;
  }
}

/** A parsed range: alternatives (||) of comparator sets (AND). */
export type VersionRange = readonly (readonly Comparator[])[];

/** Parses a range; returns null when any part is malformed. */
export function parseRange(text: string): VersionRange | null {
  const trimmed = text.trim();
  if (trimmed === "" || trimmed.length > 256) {
    return null;
  }
  const alternatives: Comparator[][] = [];
  for (const alternative of trimmed.split("||")) {
    // Join operators separated from their version by spaces (">= 1.0.0").
    const tokens = alternative
      .trim()
      .replace(/(\^|~|>=|<=|>|<|=)\s+/g, "$1")
      .split(/\s+/)
      .filter((token) => token !== "");
    if (tokens.length === 0) {
      return null;
    }
    const set: Comparator[] = [];
    for (const token of tokens) {
      const comparators = expand(token);
      if (comparators === null) {
        return null;
      }
      set.push(...comparators);
    }
    alternatives.push(set);
  }
  return alternatives;
}

/**
 * Whether a version satisfies a range. Like npm, a prerelease only matches
 * comparators that name a prerelease of the same MAJOR.MINOR.PATCH.
 */
export function satisfies(candidate: string | SemVer, range: string | VersionRange): boolean {
  const parsedVersion = typeof candidate === "string" ? parseVersion(candidate) : candidate;
  const parsedRange = typeof range === "string" ? parseRange(range) : range;
  if (parsedVersion === null || parsedRange === null) {
    return false;
  }
  return parsedRange.some((set) => {
    if (!set.every((comparator) => test(comparator, parsedVersion))) {
      return false;
    }
    if (parsedVersion.prerelease.length === 0) {
      return true;
    }
    return set.some(
      (comparator) =>
        comparator.version.prerelease.length > 0 &&
        comparator.version.prerelease[0] !== "0" &&
        comparator.version.major === parsedVersion.major &&
        comparator.version.minor === parsedVersion.minor &&
        comparator.version.patch === parsedVersion.patch,
    );
  });
}
