import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import {
  listUserSessions,
  orderSessions,
  revokeAllUserSessions,
  revokeUserSession,
  type Session,
} from "@/features/account/api";
import { useAuthStore } from "./auth";
import { resetOnUserChange } from "./userScoped";

export const useSessionsStore = defineStore("sessions", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const sessions = shallowRef<readonly Session[]>([]);
  const pending = shallowRef<ReadonlySet<string>>(new Set());

  function userId(): string {
    const id = auth.user?.id;
    if (id === undefined) {
      throw new Error("not signed in");
    }
    return id;
  }

  const list = useRequest(
    async () => {
      sessions.value = orderSessions(await listUserSessions(client, userId()), auth.sessionId);
      return sessions.value;
    },
    (data) => data.length === 0,
  );

  function setPending(id: string, on: boolean) {
    const next = new Set(pending.value);
    if (on) {
      next.add(id);
    } else {
      next.delete(id);
    }
    pending.value = next;
  }

  /**
   * Revokes one session optimistically: it disappears at once and comes back
   * if the server refuses (G31.5). Revoking this browser's own session signs
   * the user out locally as well. Returns whether that happened.
   */
  async function revoke(id: string): Promise<"revoked" | "signed-out"> {
    const owner = userId();
    const removed = sessions.value.find((session) => session.id === id);
    sessions.value = sessions.value.filter((session) => session.id !== id);
    setPending(id, true);
    try {
      await revokeUserSession(client, owner, id);
    } catch (error: unknown) {
      if (removed !== undefined && !sessions.value.some((session) => session.id === id)) {
        sessions.value = orderSessions([...sessions.value, removed], auth.sessionId);
      }
      throw error;
    } finally {
      setPending(id, false);
    }
    if (id === auth.sessionId) {
      auth.expire();
      return "signed-out";
    }
    return "revoked";
  }

  /** Signs out every device, this one included. */
  async function revokeAll(): Promise<void> {
    await revokeAllUserSessions(client, userId());
    sessions.value = [];
    auth.expire();
  }

  function reset() {
    sessions.value = [];
    pending.value = new Set();
    list.reset();
  }

  resetOnUserChange(reset);

  return { sessions, pending, state: list.state, load: list.run, revoke, revokeAll, reset };
});
