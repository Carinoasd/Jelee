import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";

export type Session = components["schemas"]["Session"];

/** Active sessions of the given user; self or administrator (server-checked). */
export async function listUserSessions(client: ApiClient, userId: string): Promise<readonly Session[]> {
  const body = await call(client.GET("/api/v1/users/{id}/sessions", { params: { path: { id: userId } } }));
  return body.data;
}

export async function revokeUserSession(client: ApiClient, userId: string, sessionId: string): Promise<void> {
  await callNoContent(
    client.DELETE("/api/v1/users/{id}/sessions/{sessionID}", { params: { path: { id: userId, sessionID: sessionId } } }),
  );
}

/** Revokes every session of the user, including the one making the request. */
export async function revokeAllUserSessions(client: ApiClient, userId: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/users/{id}/sessions", { params: { path: { id: userId } } }));
}

/** Most recently used first; the current session always leads. */
export function orderSessions(sessions: readonly Session[], currentId: string | null): Session[] {
  const lastUse = (session: Session) => Date.parse(session.lastSeenAt ?? session.createdAt) || 0;
  return [...sessions].sort((a, b) => {
    if (a.id === currentId) {
      return -1;
    }
    if (b.id === currentId) {
      return 1;
    }
    return lastUse(b) - lastUse(a);
  });
}
