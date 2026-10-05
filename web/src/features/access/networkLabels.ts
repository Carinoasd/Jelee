// Catalog keys of the network rule enumerations. Keys are spelled out in
// full so scripts/check-i18n.mjs can see that every one is used.
import type { NetworkKind, SessionKind } from "./api";

export const networkKeys: Readonly<Record<NetworkKind, string>> = {
  any: "networkRules.network.any",
  lan: "networkRules.network.lan",
  wan: "networkRules.network.wan",
};

export const sessionKindKeys: Readonly<Record<SessionKind, string>> = {
  web: "networkRules.kind.web",
  native: "networkRules.kind.native",
};
