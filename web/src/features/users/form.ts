// Pure helpers for the account forms: client-side hints only, the server
// validates everything again.
import type { ContentAccess, ContentAccessView, DeliveryLimits, User, UserLocale, UserSettings } from "./api";

/** Byte limits the server enforces on UTF-8 text (CreateUser, UserSettings). */
export const nameMaxBytes = 128;
export const passwordMinBytes = 12;
export const passwordMaxBytes = 1024;
export const tagMaxBytes = 128;
export const maxBlockedTags = 100;
/** DeliveryLimits bounds from the contract. */
export const maxConcurrentLimit = 128;
export const maxBandwidthLimit = 10_000_000;

export const userLocales: readonly UserLocale[] = ["zh-CN", "zh-TW", "ja-JP", "en-US"];

/** Catalog keys of the locale names; full literals for the i18n gate. */
export const localeNameKey: Readonly<Record<UserLocale, string>> = {
  "zh-CN": "common.localeNames.zhCN",
  "zh-TW": "common.localeNames.zhTW",
  "ja-JP": "common.localeNames.jaJP",
  "en-US": "common.localeNames.enUS",
};

export function isUserLocale(value: string): value is UserLocale {
  return (userLocales as readonly string[]).includes(value);
}

export function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length;
}

export interface NewUserForm {
  name: string;
  profileName: string;
  password: string;
  locale: UserLocale;
  admin: boolean;
  hidden: boolean;
}

export interface NewUserProblems {
  name: string | null;
  profileName: string | null;
  password: string | null;
}

/** Catalog keys of the problems found, or null per field. */
export function checkNewUser(form: NewUserForm): NewUserProblems {
  const name = form.name.trim();
  let password: string | null = null;
  const bytes = utf8Length(form.password);
  if (bytes < passwordMinBytes) {
    password = "users.create.passwordShort";
  } else if (bytes > passwordMaxBytes) {
    password = "users.create.passwordLong";
  }
  return {
    name: name === "" ? "users.form.nameRequired" : utf8Length(name) > nameMaxBytes ? "users.form.tooLong" : null,
    profileName: utf8Length(form.profileName.trim()) > nameMaxBytes ? "users.form.tooLong" : null,
    password,
  };
}

export function hasProblems(problems: object): boolean {
  return Object.values(problems).some((value) => value !== null);
}

/** Request body of a new account; the password is sent exactly as typed. */
export function newUserBody(form: NewUserForm) {
  return {
    name: form.name.trim(),
    displayName: form.profileName.trim(),
    password: form.password,
    locale: form.locale,
    admin: form.admin,
    hidden: form.hidden,
  };
}

/** Every settings field, so a PUT never resets one by omission. */
export function settingsOf(user: User): Required<UserSettings> {
  return {
    name: user.name,
    displayName: user.displayName,
    locale: user.locale,
    admin: user.admin,
    hidden: user.hidden,
    disabled: user.disabled,
  };
}

/** A limit field: "" follows the server, otherwise a whole number in range. */
export type ParsedLimit = { readonly ok: true; readonly value: number | undefined } | { readonly ok: false };

export function parseLimit(text: string, max: number): ParsedLimit {
  const trimmed = text.trim();
  if (trimmed === "") {
    return { ok: true, value: undefined };
  }
  if (!/^\d+$/.test(trimmed)) {
    return { ok: false };
  }
  const value = Number(trimmed);
  return value <= max ? { ok: true, value } : { ok: false };
}

export function limitText(value: number | undefined): string {
  return value === undefined ? "" : String(value);
}

/** Request body of the delivery limits; empty fields are omitted. */
export function limitsBody(maxConcurrent: number | undefined, bandwidth: number | undefined): DeliveryLimits {
  return {
    ...(maxConcurrent !== undefined ? { maxStreams: maxConcurrent } : {}),
    ...(bandwidth !== undefined ? { maxKbps: bandwidth } : {}),
  };
}

/** Three choices for items without a recognized rating. */
export type UnratedChoice = "policy" | "hide" | "show";

export function unratedChoice(value: boolean | undefined): UnratedChoice {
  return value === undefined ? "policy" : value ? "hide" : "show";
}

/** Ceiling select value: "" is no ceiling. */
export function ceilingText(value: number | undefined): string {
  return value === undefined ? "" : String(value);
}

/** Request body of the content access settings; item rules are not part of it. */
export function contentAccessBody(ceiling: string, unrated: UnratedChoice, tags: readonly string[]): ContentAccess {
  return {
    ...(ceiling !== "" ? { parentalRatingMax: Number(ceiling) } : {}),
    ...(unrated !== "policy" ? { blockUnrated: unrated === "hide" } : {}),
    blockedTags: [...tags],
  };
}

export function contentAccessOf(view: ContentAccessView): ContentAccess {
  return contentAccessBody(ceilingText(view.parentalRatingMax), unratedChoice(view.blockUnrated), view.blockedTags);
}

/** Checks a tag to add: catalog key of the problem or null. */
export function checkTag(tag: string, existing: readonly string[]): string | null {
  const value = tag.trim();
  if (value === "") {
    return "users.content.tagEmpty";
  }
  if (utf8Length(value) > tagMaxBytes) {
    return "users.form.tooLong";
  }
  // The server compares tags trimmed and case-insensitively.
  if (existing.some((entry) => entry.toLowerCase() === value.toLowerCase())) {
    return "users.content.tagDuplicate";
  }
  if (existing.length >= maxBlockedTags) {
    return "users.content.tagLimit";
  }
  return null;
}
