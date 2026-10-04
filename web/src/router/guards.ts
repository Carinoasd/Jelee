import type { LocationQuery, RouteLocationRaw, RouteMeta, RouteRecordNameGeneric } from "vue-router";
import { safeRedirect } from "./redirect";

/** The parts of a target route the policy reads. */
export interface GuardTarget {
  readonly name?: RouteRecordNameGeneric | null;
  readonly meta: RouteMeta;
  readonly query: LocationQuery;
  readonly fullPath: string;
}

export interface SessionView {
  readonly isAuthenticated: boolean;
  readonly isAdmin: boolean;
  /** A share guest: a restricted session that only browses its share. */
  readonly isGuest?: boolean;
}

/**
 * Client-side navigation policy. It only shapes the experience: every API
 * call is authorized again by the server (G35.2).
 */
export function navigationGuard(to: GuardTarget, session: SessionView): true | RouteLocationRaw {
  if (to.name === "login" && session.isAuthenticated) {
    return safeRedirect(to.query.redirect);
  }
  if (to.meta.public === true) {
    return true;
  }
  if (!session.isAuthenticated) {
    return { name: "login", query: { redirect: to.fullPath } };
  }
  if (session.isGuest === true && to.meta.guest !== true) {
    return { name: "shared" };
  }
  if (to.meta.admin === true && !session.isAdmin) {
    return { name: "forbidden" };
  }
  return true;
}
