import type { ItemAccessEffect, User } from "./api";
import type { UnratedChoice } from "./form";

export type UserStatus = "active" | "disabled" | "deleted";

export function userStatusOf(user: User): UserStatus {
  if (user.deletedAt !== undefined) {
    return "deleted";
  }
  return user.disabled ? "disabled" : "active";
}

/** Catalog keys; full literals so the i18n gate can see them. */
export const userStatusKey: Readonly<Record<UserStatus, string>> = {
  active: "users.status.active",
  disabled: "users.status.disabled",
  deleted: "users.status.deleted",
};

export const effectKey: Readonly<Record<ItemAccessEffect, string>> = {
  hide: "users.rules.effectHide",
  allow: "users.rules.effectAllow",
};

export const unratedKey: Readonly<Record<UnratedChoice, string>> = {
  policy: "users.content.unratedPolicy",
  hide: "users.content.unratedHide",
  show: "users.content.unratedShow",
};
