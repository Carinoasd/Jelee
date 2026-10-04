// Catalog keys of the client control enumerations. Keys are spelled out in
// full so scripts/check-i18n.mjs can see that every one is used.
import { ApiError, networkError } from "@/api/errors";
import type { HitMode, RuleAction, RuleDimension, RuleIntent, RuleMatch, UnknownClientsPolicy } from "./api";

export const dimensionKeys: Readonly<Record<RuleDimension, string>> = {
  user_agent: "clients.dimension.user_agent",
  app_name: "clients.dimension.app_name",
  app_version: "clients.dimension.app_version",
  device_id: "clients.dimension.device_id",
  device_name: "clients.dimension.device_name",
  device_type: "clients.dimension.device_type",
  ip: "clients.dimension.ip",
  api_key_fingerprint: "clients.dimension.api_key_fingerprint",
  header: "clients.dimension.header",
};

export const matchKeys: Readonly<Record<RuleMatch, string>> = {
  exact: "clients.match.exact",
  prefix: "clients.match.prefix",
  glob: "clients.match.glob",
  regex: "clients.match.regex",
  cidr: "clients.match.cidr",
  absent: "clients.match.absent",
};

export const ruleActionKeys: Readonly<Record<RuleAction, string>> = {
  allow: "clients.action.allow",
  deny: "clients.action.deny",
  read_only: "clients.action.read_only",
  rate_limit: "clients.action.rate_limit",
  force_relogin: "clients.action.force_relogin",
  restrict_libraries: "clients.action.restrict_libraries",
  observe: "clients.action.observe",
  shadow: "clients.action.shadow",
};

/** Rule actions plus pending_approval, which hits of the default policy report. */
export const actionKeys: Readonly<Record<RuleAction | "pending_approval", string>> = {
  ...ruleActionKeys,
  pending_approval: "clients.action.pending_approval",
};

export const intentKeys: Readonly<Record<RuleIntent, string>> = {
  allow: "clients.action.allow",
  deny: "clients.action.deny",
  read_only: "clients.action.read_only",
  rate_limit: "clients.action.rate_limit",
  force_relogin: "clients.action.force_relogin",
  restrict_libraries: "clients.action.restrict_libraries",
};

export const unknownClientsKeys: Readonly<Record<UnknownClientsPolicy, { label: string; impact: string }>> = {
  allow: { label: "clients.unknown.allow", impact: "clients.unknownImpact.allow" },
  read_only: { label: "clients.unknown.read_only", impact: "clients.unknownImpact.read_only" },
  deny: { label: "clients.unknown.deny", impact: "clients.unknownImpact.deny" },
  pending_approval: { label: "clients.unknown.pending_approval", impact: "clients.unknownImpact.pending_approval" },
};

export const modeKeys: Readonly<Record<HitMode, string>> = {
  enforced: "clients.mode.enforced",
  exempt: "clients.mode.exempt",
  observe: "clients.mode.observe",
  shadow: "clients.mode.shadow",
  default: "clients.mode.default",
};

export const surfaceKeys = {
  native: "clients.surface.native",
  compat: "clients.surface.compat",
} as const;

export const clientKindKeys = {
  web: "clients.kind.web",
  native: "clients.kind.native",
} as const;

/** Looks up a server-provided code; unknown codes are shown as they are. */
export function codeLabel(keys: Readonly<Record<string, string>>, code: string, t: (key: string) => string): string {
  const key = keys[code];
  return key === undefined ? code : t(key);
}

export function asApiError(error: unknown): ApiError {
  return error instanceof ApiError ? error : networkError(error);
}
