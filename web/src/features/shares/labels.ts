// Catalog keys of the share link enumerations. Keys are spelled out in full
// so scripts/check-i18n.mjs can see that every one is used.
import type { ShareEvent, ShareRefusal, ShareState } from "./api";

export const stateKeys: Readonly<Record<ShareState, string>> = {
  active: "shares.state.active",
  expired: "shares.state.expired",
  revoked: "shares.state.revoked",
};

export const eventKeys: Readonly<Record<ShareEvent, string>> = {
  "share.created": "shares.access.event.created",
  "share.revoked": "shares.access.event.revoked",
  "share.redeemed": "shares.access.event.redeemed",
  "share.redeem_refused": "shares.access.event.redeemRefused",
  "share.accessed": "shares.access.event.accessed",
  "share.access_refused": "shares.access.event.accessRefused",
};

export const refusalKeys: Readonly<Record<ShareRefusal, string>> = {
  revoked: "shares.access.reason.revoked",
  expired: "shares.access.reason.expired",
  playback: "shares.access.reason.native",
  session_limit: "shares.access.reason.sessionLimit",
};

export const clientKindKeys = {
  web: "shares.access.kind.web",
  native: "shares.access.kind.native",
} as const;

/** Lifetimes the create form offers; "custom" asks for a date and time. */
export const lifetimes = {
  hour: { ms: 60 * 60 * 1000, key: "shares.create.lifetime.hour" },
  day: { ms: 24 * 60 * 60 * 1000, key: "shares.create.lifetime.day" },
  week: { ms: 7 * 24 * 60 * 60 * 1000, key: "shares.create.lifetime.week" },
  month: { ms: 30 * 24 * 60 * 60 * 1000, key: "shares.create.lifetime.month" },
  // A minute short of the server's 90-day maximum, for clock skew and the
  // time the request takes.
  longest: { ms: 90 * 24 * 60 * 60 * 1000 - 60 * 1000, key: "shares.create.lifetime.longest" },
  custom: { ms: 0, key: "shares.create.lifetime.custom" },
} as const;
export type Lifetime = keyof typeof lifetimes;
