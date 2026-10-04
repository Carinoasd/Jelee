import type { ItemMetadata } from "./api";

type FieldName = ItemMetadata["fields"][number]["field"];

export interface ExternalId {
  readonly type: string;
  readonly value: string;
  readonly isDefault: boolean;
}

/** Display-oriented view of the administrator metadata response. */
export interface MetadataSummary {
  readonly originalTitle: string;
  readonly tagline: string;
  readonly overview: string;
  readonly year: number | null;
  readonly genres: readonly string[];
  readonly externalIds: readonly ExternalId[];
  /** Displayed fields whose current value came from an NFO file. */
  readonly nfoFields: ReadonlySet<string>;
  readonly nfoStatus: "valid" | "missing" | "nfo_invalid" | null;
  readonly nfoReadAt: string | null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === "string" && entry !== "") : [];
}

function externalIds(value: unknown): ExternalId[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const result: ExternalId[] = [];
  for (const entry of value as unknown[]) {
    if (isRecord(entry) && typeof entry.type === "string" && typeof entry.value === "string") {
      result.push({ type: entry.type, value: entry.value, isDefault: entry.default === true });
    }
  }
  return result;
}

/** Year from a fact, else the leading four digits of the release date. */
function yearOf(fact: unknown, date: string): number | null {
  if (typeof fact === "number" && Number.isInteger(fact) && fact > 0) {
    return fact;
  }
  const match = /^(\d{4})-/.exec(date);
  return match ? Number(match[1]) : null;
}

/**
 * Narrows the metadata response to what the detail page shows. Values are
 * read defensively from the loosely typed fact union; anything unexpected is
 * skipped rather than rendered.
 */
export function summarizeMetadata(metadata: ItemMetadata): MetadataSummary {
  const fields = new Map(metadata.fields.map((field) => [field.field, field]));
  const facts = new Map<string, { value: unknown; source: string }>(
    metadata.facts.map((fact) => [fact.field, { value: fact.value, source: fact.source }]),
  );
  const text = (name: FieldName) => fields.get(name)?.value.trim() ?? "";
  const nfoFields = new Set<string>();
  for (const field of metadata.fields) {
    if (field.source === "nfo" && field.value.trim() !== "") {
      nfoFields.add(field.field);
    }
  }
  for (const [name, fact] of facts) {
    if (fact.source === "nfo") {
      nfoFields.add(name);
    }
  }
  const observation = metadata.lastConfirmedNFOObservation;
  return {
    originalTitle: text("originalTitle"),
    tagline: text("tagline"),
    overview: text("overview"),
    year: yearOf(facts.get("year")?.value, text("date")),
    genres: stringList(facts.get("genres")?.value),
    externalIds: externalIds(facts.get("uniqueIds")?.value),
    nfoFields,
    nfoStatus: observation?.status ?? null,
    nfoReadAt: observation?.readAt ?? null,
  };
}
