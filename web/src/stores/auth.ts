import { defineStore } from "pinia";
import { computed, shallowRef } from "vue";
import { useApi } from "@/api";
import {
  currentUser,
  login as loginRequest,
  logout as logoutRequest,
  readCsrf,
  redeemShare as redeemRequest,
  type SecondFactorChallenge,
  type SessionGrant,
  type User,
} from "@/features/auth/api";

// Holds the signed-in user and the current session's ID only. Credential
// material stays inside the API layer's AuthStrategy so it never appears in
// reactive or devtools state.
export const useAuthStore = defineStore("auth", () => {
  const { client, auth } = useApi();
  const user = shallowRef<User | null>(null);
  /** ID of this browser's session; unknown after a reload until the next login. */
  const sessionId = shallowRef<string | null>(null);
  const isAuthenticated = computed(() => user.value !== null);
  const isAdmin = computed(() => user.value?.admin === true);
  /**
   * A share guest. Decided by the account name: the server names every share
   * guest account "share:<share ID>", and the name is known synchronously
   * after a reload (GET /users/me), unlike GET /shares/current, which would
   * need a request before the first navigation. Only shapes the UI: the
   * server refuses guests everything outside their share (share_forbidden).
   */
  const isGuest = computed(() => user.value?.name.startsWith("share:") === true);
  let restoring: Promise<void> | null = null;

  /**
   * Resumes a session that survived a page reload (the HttpOnly cookie).
   * Runs at most once; any failure simply leaves the visitor signed out.
   */
  function restore(): Promise<void> {
    restoring ??= (async () => {
      if (!auth.survivesReload || user.value !== null) {
        return;
      }
      try {
        const me = await currentUser(client);
        auth.resume(await readCsrf(client));
        user.value = me;
      } catch {
        auth.clear();
      }
    })();
    return restoring;
  }

  /** Signs in, or returns the challenge when the account has a second factor (G07.8). */
  async function login(name: string, password: string): Promise<User | SecondFactorChallenge> {
    const result = await loginRequest(client, name, password);
    return "secondFactorRequired" in result ? result : establish(result);
  }

  /** Takes over a session granted by the password login or its second step. */
  function establish(grant: SessionGrant): User {
    auth.establish(grant);
    restoring = Promise.resolve();
    user.value = grant.user;
    sessionId.value = grant.session.id;
    return grant.user;
  }

  /** Opens a share link: the browser's session becomes the share's guest. */
  async function redeemShare(token: string): Promise<User> {
    return establish(await redeemRequest(client, token));
  }

  async function logout(): Promise<void> {
    try {
      if (auth.hasCredential()) {
        await logoutRequest(client);
      }
    } finally {
      expire();
    }
  }

  /** Forgets the session locally, e.g. after the server answered 401. */
  function expire(): void {
    auth.clear();
    restoring = Promise.resolve();
    user.value = null;
    sessionId.value = null;
  }

  return { user, sessionId, isAuthenticated, isAdmin, isGuest, restore, login, establish, redeemShare, logout, expire };
});
