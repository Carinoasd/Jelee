// Pure helpers of the rating code editor (G48.7). The table is replaced as
// a whole; the server trims and upper-cases the codes and validates again.
import type { ParentalRating, RatingLevel } from "./api";

export const maxRatingCodes = 500;
export const ratingCodeMaxChars = 32;
export const ratingLevelMax = 21;

export interface RatingRow {
  /** Local key for list rendering; never sent. */
  readonly key: number;
  code: string;
  /** Level select value, a minimum age. */
  level: string;
}

let nextKey = 0;

/** Length in Unicode code points, as the server counts characters. */
export function codePoints(value: string): number {
  return Array.from(value).length;
}

export function ratingRow(code = "", level = 0): RatingRow {
  return { key: ++nextKey, code, level: String(level) };
}

/** Editor rows of the current table, lowest level first. */
export function ratingRowsOf(levels: readonly RatingLevel[]): RatingRow[] {
  return levels.flatMap((level) => level.codes.map((code) => ratingRow(code, level.level)));
}

/** A code as the server stores it. */
export function normalizeRatingCode(code: string): string {
  return code.trim().toUpperCase();
}

/**
 * Catalog key of the problem with one row, or null. The server refuses
 * codes it could never match: a "Rated " prefix or a two-letter country
 * prefix such as "US:" is stripped from metadata before matching.
 */
export function ratingRowProblem(row: RatingRow, earlier: readonly RatingRow[]): string | null {
  const code = normalizeRatingCode(row.code);
  if (code === "") {
    return "contentRules.ratings.codeEmpty";
  }
  if (codePoints(code) > ratingCodeMaxChars) {
    return "contentRules.ratings.codeTooLong";
  }
  if (/^RATED\s/.test(code) || /^[A-Z]{2}:/.test(code)) {
    return "contentRules.ratings.codePrefix";
  }
  if (earlier.some((other) => normalizeRatingCode(other.code) === code)) {
    return "contentRules.ratings.codeDuplicate";
  }
  const level = Number(row.level);
  if (!/^\d+$/.test(row.level) || level > ratingLevelMax) {
    return "contentRules.ratings.levelInvalid";
  }
  return null;
}

/** Problems by row key; rows without a problem are absent. */
export function ratingProblems(rows: readonly RatingRow[]): Map<number, string> {
  const problems = new Map<number, string>();
  rows.forEach((row, index) => {
    const problem = ratingRowProblem(row, rows.slice(0, index));
    if (problem !== null) {
      problems.set(row.key, problem);
    }
  });
  return problems;
}

export function ratingsBody(rows: readonly RatingRow[]): ParentalRating[] {
  return rows.map((row) => ({ code: normalizeRatingCode(row.code), level: Number(row.level) }));
}
