// Pure helpers of the access template editor (G48.7); the server validates
// everything again.
import type { UnratedChoice } from "@/features/users/form";
import { templateNameMaxChars, type AccessTemplate, type AccessTemplateInput } from "./api";
import { codePoints } from "./ratings";

export interface TemplateFields {
  readonly name: string;
  readonly libraryIds: readonly string[];
  /** Ceiling select value: "" is no ceiling. */
  readonly ceiling: string;
  readonly unrated: UnratedChoice;
  readonly tags: readonly string[];
  readonly keywords: readonly string[];
}

/** Request body of a template; the name is trimmed, empty choices omitted. */
export function templateInput(fields: TemplateFields): AccessTemplateInput {
  return {
    name: fields.name.trim(),
    libraryIds: [...fields.libraryIds],
    ...(fields.ceiling !== "" ? { parentalRatingMax: Number(fields.ceiling) } : {}),
    ...(fields.unrated !== "policy" ? { blockUnrated: fields.unrated === "hide" } : {}),
    blockedTags: [...fields.tags],
    blockedKeywords: [...fields.keywords],
  };
}

/** Catalog key of the problem with a template name, or null. Names are unique case-insensitively. */
export function templateNameProblem(name: string, existing: readonly AccessTemplate[], ownId: string | null): string | null {
  const value = name.trim();
  if (value === "") {
    return "accessMatrix.templates.nameRequired";
  }
  if (codePoints(value) > templateNameMaxChars) {
    return "accessMatrix.templates.nameTooLong";
  }
  const key = value.toLowerCase();
  if (existing.some((template) => template.id !== ownId && template.name.toLowerCase() === key)) {
    return "accessMatrix.templates.nameTaken";
  }
  return null;
}
