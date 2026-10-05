import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { listUserSessions, orderSessions, revokeAllUserSessions, type Session } from "@/features/account/api";
import type { LibrarySummary } from "@/features/libraries/api";
import {
  deleteUser,
  getDeliveryLimits,
  getUser,
  listAllLibraries,
  listGrants,
  putDeliveryLimits,
  putGrants,
  restoreUser,
  setNativeAccess,
  unlockUser,
  updateUser,
  type DeliveryLimits,
  type User,
  type UserSettings,
} from "@/features/users/api";
import { useAuthStore } from "./auth";
import { resetOnUserChange } from "./userScoped";

/** Whether a change ended the administrator's own session. */
export type ChangeOutcome = "done" | "signed-out";

/** Mutations in flight, so each section can show its own busy state. */
export type UserAdminAction = "settings" | "status" | "native" | "limits" | "unlock" | "delete" | "restore" | "sessions" | "libraries";

// One account in the administration pages: profile and settings, native
// login, delivery limits, lifecycle, sessions and library grants. Content
// access lives in userAdminContent.
export const useUserAdminStore = defineStore("userAdmin", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const userId = shallowRef("");
  const user = shallowRef<User | null>(null);
  const limits = shallowRef<DeliveryLimits>({});
  const sessions = shallowRef<readonly Session[]>([]);
  const libraries = shallowRef<readonly LibrarySummary[]>([]);
  const grants = shallowRef<ReadonlySet<string>>(new Set());
  const busy = shallowRef<UserAdminAction | null>(null);

  const userRequest = useRequest(async () => {
    user.value = await getUser(client, userId.value);
    return user.value;
  });
  const limitsRequest = useRequest(async () => {
    limits.value = await getDeliveryLimits(client, userId.value);
    return limits.value;
  });
  const sessionsRequest = useRequest(
    async () => {
      sessions.value = orderSessions(await listUserSessions(client, userId.value), null);
      return sessions.value;
    },
    (list) => list.length === 0,
  );
  const librariesRequest = useRequest(
    async () => {
      const [all, granted] = await Promise.all([listAllLibraries(client), listGrants(client, userId.value)]);
      libraries.value = all;
      grants.value = new Set(granted.map((grant) => grant.libraryId));
      return all;
    },
    (list) => list.length === 0,
  );

  function reset() {
    userId.value = "";
    user.value = null;
    limits.value = {};
    sessions.value = [];
    libraries.value = [];
    grants.value = new Set();
    busy.value = null;
    userRequest.reset();
    limitsRequest.reset();
    sessionsRequest.reset();
    librariesRequest.reset();
  }

  /** Switches to an account and loads its profile. */
  async function open(id: string): Promise<void> {
    if (id !== userId.value) {
      reset();
      userId.value = id;
    }
    await userRequest.run();
  }

  async function busyWith<T>(action: UserAdminAction, work: () => Promise<T>): Promise<T> {
    busy.value = action;
    try {
      return await work();
    } finally {
      busy.value = null;
    }
  }

  /** The server revoked every session of the target; if that is us, sign out locally. */
  function afterRevokingAll(target: string): ChangeOutcome {
    if (target === auth.user?.id) {
      auth.expire();
      return "signed-out";
    }
    return "done";
  }

  /**
   * Replaces the settings. Changing the role or disabling the account makes
   * the server revoke every session of it.
   */
  async function saveSettings(settings: Required<UserSettings>, action: UserAdminAction = "settings"): Promise<ChangeOutcome> {
    const target = userId.value;
    const before = user.value;
    const updated = await busyWith(action, () => updateUser(client, target, settings));
    user.value = updated;
    if (before !== null && (before.admin !== updated.admin || updated.disabled)) {
      sessions.value = [];
      return afterRevokingAll(target);
    }
    return "done";
  }

  /** Withdrawing native login also revokes the native sessions. */
  async function setNative(allow: boolean): Promise<void> {
    const target = userId.value;
    user.value = await busyWith("native", () => setNativeAccess(client, target, allow));
    if (!allow) {
      sessions.value = sessions.value.filter((session) => session.clientKind !== "native");
    }
  }

  async function saveLimits(next: DeliveryLimits): Promise<void> {
    const target = userId.value;
    limits.value = await busyWith("limits", () => putDeliveryLimits(client, target, next));
  }

  async function unlock(): Promise<void> {
    const target = userId.value;
    await busyWith("unlock", () => unlockUser(client, target));
  }

  /** Soft delete: signs the account out everywhere; reload shows deletedAt. */
  async function remove(): Promise<ChangeOutcome> {
    const target = userId.value;
    await busyWith("delete", () => deleteUser(client, target));
    sessions.value = [];
    const outcome = afterRevokingAll(target);
    if (outcome === "done") {
      await userRequest.run();
    }
    return outcome;
  }

  async function restore(): Promise<void> {
    const target = userId.value;
    user.value = await busyWith("restore", () => restoreUser(client, target));
  }

  async function revokeAllSessions(): Promise<ChangeOutcome> {
    const target = userId.value;
    await busyWith("sessions", () => revokeAllUserSessions(client, target));
    sessions.value = [];
    return afterRevokingAll(target);
  }

  async function saveGrants(libraryIds: readonly string[]): Promise<void> {
    const target = userId.value;
    await busyWith("libraries", () => putGrants(client, target, libraryIds));
    grants.value = new Set(libraryIds);
  }

  resetOnUserChange(reset);

  return {
    userId,
    user,
    limits,
    sessions,
    libraries,
    grants,
    busy,
    userState: userRequest.state,
    limitsState: limitsRequest.state,
    sessionsState: sessionsRequest.state,
    librariesState: librariesRequest.state,
    open,
    reload: userRequest.run,
    loadLimits: limitsRequest.run,
    loadSessions: sessionsRequest.run,
    loadLibraries: librariesRequest.run,
    saveSettings,
    setNative,
    saveLimits,
    unlock,
    remove,
    restore,
    revokeAllSessions,
    saveGrants,
    reset,
  };
});
