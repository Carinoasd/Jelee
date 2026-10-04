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
  /** Developer mode is on; only read for routes with meta.devMode. */
  readonly devMode?: boolean;
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
  if (to.meta.devMode === true && session.devMode !== true) {
    // Outside developer mode the page does not exist (G45.8).
    return { name: "not-found", params: { pathMatch: to.fullPath.split(/[?#]/)[0]?.split("/").filter((segment) => segment !== "") ?? [] } };
  }
  return true;
}
